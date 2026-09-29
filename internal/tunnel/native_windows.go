//go:build windows

package tunnel

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wintun"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

type windowsRing struct {
	adapter               *wintun.Adapter
	session               wintun.Session
	readEvent, closeEvent windows.Handle
}

func (r *windowsRing) receive() ([]byte, error) {
	packet, err := r.session.ReceivePacket()
	if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
		return nil, errRingEmpty
	}
	return packet, ringWindowsError(err)
}
func (r *windowsRing) release(packet []byte) { r.session.ReleaseReceivePacket(packet) }
func (r *windowsRing) allocate(size int) ([]byte, error) {
	packet, err := r.session.AllocateSendPacket(size)
	// Ring congestion rejects this packet, without retiring a healthy device.
	if errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) {
		return nil, err
	}
	return packet, ringWindowsError(err)
}
func (r *windowsRing) send(packet []byte) { r.session.SendPacket(packet) }
func (r *windowsRing) wait() error {
	status, err := windows.WaitForMultipleObjects([]windows.Handle{r.closeEvent, r.readEvent}, false, 1000)
	if err != nil {
		return fmt.Errorf("%w: wait for Wintun: %w", ErrUnavailable, err)
	}
	switch status {
	case windows.WAIT_OBJECT_0:
		return os.ErrClosed
	case windows.WAIT_OBJECT_0 + 1, uint32(windows.WAIT_TIMEOUT):
		return nil
	default:
		return fmt.Errorf("%w: unexpected Wintun wait status %d", ErrUnavailable, status)
	}
}
func (r *windowsRing) wake() error { return windows.SetEvent(r.closeEvent) }
func (r *windowsRing) close() error {
	r.session.End()
	// WintunCloseAdapter is a void API. The Go binding's apparent last-error
	// return is not an API result and must not be reported as a cleanup failure.
	_ = r.adapter.Close()
	return windows.CloseHandle(r.closeEvent)
}
func ringWindowsError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: Wintun I/O: %w", ErrUnavailable, err)
}

type windowsDevice struct {
	*ringDevice
	luid  winipcfg.LUID
	index int
}

// Wintun may rename an existing adapter to make room for a requested alias.
// Serialize GraphWAN creators across processes and reject occupied aliases first.
// A Windows mutex belongs to an OS thread, so pin that thread until release.
func reserveWindowsName(name string) (func(), error) {
	hash := sha256.Sum256([]byte(strings.ToLower(name)))
	mutexName, _ := windows.UTF16PtrFromString(fmt.Sprintf(`Global\GraphWAN.TUN.%x`, hash))
	mutex, err := windows.CreateMutex(nil, false, mutexName)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return nil, err
	}
	runtime.LockOSThread()
	status, err := windows.WaitForSingleObject(mutex, 10000)
	if err != nil || (status != windows.WAIT_OBJECT_0 && status != windows.WAIT_ABANDONED) {
		runtime.UnlockOSThread()
		windows.CloseHandle(mutex)
		return nil, fmt.Errorf("reserve TUN name (status %d): %w", status, errors.Join(err, errors.New("name reservation failed")))
	}
	release := func() { _ = windows.ReleaseMutex(mutex); _ = windows.CloseHandle(mutex); runtime.UnlockOSThread() }
	interfaces, err := winipcfg.GetIfTable2Ex(winipcfg.MibIfEntryNormal)
	if err != nil {
		release()
		return nil, err
	}
	for _, iface := range interfaces {
		if strings.EqualFold(iface.Alias(), name) {
			release()
			return nil, fmt.Errorf("TUN name %q is already in use", name)
		}
	}
	return release, nil
}

func Open(config Config) (Device, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	config = config.withName()
	release, err := reserveWindowsName(config.Name)
	if err != nil {
		return nil, err
	}
	defer release()
	dll, err := windows.LoadLibraryEx("wintun.dll", 0, windows.LOAD_LIBRARY_SEARCH_APPLICATION_DIR|windows.LOAD_LIBRARY_SEARCH_SYSTEM32)
	if err != nil {
		return nil, fmt.Errorf("load wintun.dll beside the executable: %w", err)
	}
	defer windows.FreeLibrary(dll)
	// The upstream Go binding panics when an optional lazy symbol is absent.
	// Validate the complete required ABI before allocating a kernel adapter.
	for _, name := range []string{"WintunCreateAdapter", "WintunCloseAdapter", "WintunGetAdapterLUID", "WintunSetLogger", "WintunStartSession", "WintunEndSession", "WintunGetReadWaitEvent", "WintunReceivePacket", "WintunReleaseReceivePacket", "WintunAllocateSendPacket", "WintunSendPacket"} {
		if _, err := windows.GetProcAddress(dll, name); err != nil {
			return nil, fmt.Errorf("incompatible wintun.dll: missing %s: %w", name, err)
		}
	}
	adapter, err := wintun.CreateAdapter(config.Name, "GraphWAN", nil)
	if err != nil {
		return nil, fmt.Errorf("create Wintun (requires administrator and matching wintun.dll beside the executable): %w", err)
	}
	success := false
	defer func() {
		if !success {
			_ = adapter.Close()
		}
	}()
	session, err := adapter.StartSession(1 << 20) // Bounded 1 MiB rings per Network.
	if err != nil {
		return nil, fmt.Errorf("start Wintun session: %w", err)
	}
	defer func() {
		if !success {
			session.End()
		}
	}()
	closeEvent, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if !success {
			_ = windows.CloseHandle(closeEvent)
		}
	}()
	luid := winipcfg.LUID(adapter.LUID())
	iface, err := luid.Interface()
	if err != nil {
		return nil, err
	}
	if iface.Alias() != config.Name {
		return nil, errors.New("created Wintun alias does not match requested name")
	}
	readEvent := session.ReadWaitEvent()
	if readEvent == 0 || readEvent == windows.InvalidHandle {
		return nil, errors.New("Wintun returned an invalid read event")
	}
	d := &windowsDevice{ringDevice: &ringDevice{config: config,
		ring: &windowsRing{adapter: adapter, session: session, readEvent: readEvent, closeEvent: closeEvent}}, luid: luid, index: int(iface.InterfaceIndex)}
	if err := d.requireFamily(config.Address.Addr()); err != nil {
		return nil, err
	}
	if err := d.setMTU(config.MTU); err != nil {
		return nil, err
	}
	if err := luid.AddIPAddress(config.Address); err != nil {
		return nil, fmt.Errorf("configure Wintun address: %w", err)
	}
	if err := d.routes().ensure(config.Address); err != nil {
		return nil, err
	}
	success = true
	return d, nil
}

func addressFamily(address netip.Addr) winipcfg.AddressFamily {
	if address.Is4() {
		return windows.AF_INET
	}
	return windows.AF_INET6
}
func (d *windowsDevice) requireFamily(address netip.Addr) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := d.luid.IPInterface(addressFamily(address))
		if err == nil {
			return nil
		}
		if !errors.Is(err, windows.ERROR_NOT_FOUND) || time.Now().After(deadline) {
			return fmt.Errorf("Wintun IP stack is unavailable: %w", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (d *windowsDevice) setMTU(mtu int) error {
	var previous []winipcfg.MibIPInterfaceRow
	for _, family := range []winipcfg.AddressFamily{windows.AF_INET, windows.AF_INET6} {
		row, err := d.luid.IPInterface(family)
		if errors.Is(err, windows.ERROR_NOT_FOUND) {
			continue
		}
		if err != nil {
			return err
		}
		previous = append(previous, *row)
	}
	if len(previous) == 0 {
		return fmt.Errorf("%w: Wintun IP interfaces disappeared", ErrUnavailable)
	}
	var changes []settingChange
	for _, old := range previous {
		row := old
		row.NLMTU = uint32(mtu)
		row.RouterDiscoveryBehavior = winipcfg.RouterDiscoveryDisabled
		row.DadTransmits = 0
		row.ManagedAddressConfigurationSupported, row.OtherStatefulConfigurationSupported = false, false
		row.UseAutomaticMetric, row.Metric = false, 0
		changes = append(changes, settingChange{apply: row.Set, restore: old.Set})
	}
	return changeSettings(changes)
}

func (d *windowsDevice) Reconfigure(config Config) error {
	d.configMu.Lock()
	defer d.configMu.Unlock()
	return d.reconfigureLocked(config)
}
func (d *windowsDevice) SetMTU(mtu int) error {
	d.configMu.Lock()
	defer d.configMu.Unlock()
	config := d.config
	config.MTU = mtu
	return d.reconfigureLocked(config)
}
func (d *windowsDevice) reconfigureLocked(config Config) error {
	if d.closed.Load() {
		return os.ErrClosed
	}
	if config.Name == "" {
		config.Name = d.config.Name
	}
	if err := config.Validate(); err != nil {
		return err
	}
	if config.Name != d.config.Name {
		return errors.New("cannot rename an owned Wintun adapter")
	}
	if err := d.requireFamily(config.Address.Addr()); err != nil {
		return err
	}
	// Keep both address-family MTUs aligned, including a newly enabled family.
	if err := d.setMTU(d.config.MTU); err != nil {
		return err
	}
	ops := configOperations{setMTU: d.setMTU,
		addresses: func() ([]netip.Prefix, error) {
			rows, err := winipcfg.GetUnicastIPAddressTable(windows.AF_UNSPEC)
			if err != nil {
				return nil, err
			}
			var result []netip.Prefix
			for _, row := range rows {
				if row.InterfaceLUID == d.luid {
					result = append(result, netip.PrefixFrom(row.Address.Addr(), int(row.OnLinkPrefixLength)))
				}
			}
			return result, nil
		},
		add: d.luid.AddIPAddress, remove: d.luid.DeleteIPAddress,
	}
	if err := changeRoutedConfig(d.config, config, ops, d.routes()); err != nil {
		return err
	}
	d.config.Address, d.config.MTU = config.Address, config.MTU
	return nil
}

func (d *windowsDevice) routes() subnetRoutes {
	nextHop := func(prefix netip.Prefix) netip.Addr {
		if prefix.Addr().Is4() {
			return netip.IPv4Unspecified()
		}
		return netip.IPv6Unspecified()
	}
	return subnetRoutes{index: d.index,
		list: func(prefix netip.Prefix) ([]routeEntry, error) {
			rows, err := winipcfg.GetIPForwardTable2(addressFamily(prefix.Addr()))
			if err != nil {
				return nil, err
			}
			var result []routeEntry
			for _, row := range rows {
				// Windows permits routes on several LUIDs for the same prefix.
				// Both inspection and mutation are confined to this owned LUID.
				if row.InterfaceLUID == d.luid && row.NextHop.Addr().IsUnspecified() {
					result = append(result, routeEntry{prefix: row.DestinationPrefix.Prefix(), index: d.index, usable: true})
				}
			}
			return result, nil
		},
		add:    func(prefix netip.Prefix) error { return d.luid.AddRoute(prefix, nextHop(prefix), 0) },
		remove: func(prefix netip.Prefix) error { return d.luid.DeleteRoute(prefix, nextHop(prefix)) },
	}
}
