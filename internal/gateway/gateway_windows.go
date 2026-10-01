package gateway

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"

	"github.com/eWloYW8/GraphWAN/internal/model"
)

func applyPlatform(ctx context.Context, id model.ID, entries []Entry) error {
	type rule struct{ Interface, Overlay, Subnet, Address, Family string }
	rules := []rule{}
	for _, e := range entries {
		if e.Mode == model.GatewaySNAT {
			return errors.New("SNAT gateway mode is currently supported only on Linux; use route or off on Windows")
		}
		if e.Mode == model.GatewayOff {
			continue
		}
		family := "IPv6"
		address := e.Subnet.Addr().String()
		if e.Overlay.Addr().Is4() {
			family = "IPv4"
		}
		if e.Subnet.Bits() == 0 {
			address = "2001:4860:4860::8888"
			if family == "IPv4" {
				address = "1.1.1.1"
			}
		}
		rules = append(rules, rule{e.Interface, e.Overlay.String(), e.Subnet.String(), address, family})
	}
	raw, err := json.Marshal(rules)
	if err != nil {
		return err
	}
	group := "GraphWAN-Gateway-" + string(id)
	script := fmt.Sprintf(`
$ErrorActionPreference = 'Stop'
$group = '%s'
$rules = ConvertFrom-Json '%s'
$indexes = @()
foreach ($r in $rules) {
 $tun = @(Get-NetIPInterface -InterfaceAlias $r.Interface -AddressFamily $r.Family -ErrorAction Stop)
 $route = @(Find-NetRoute -RemoteIPAddress $r.Address -ErrorAction Stop)
 if ($tun.Count -eq 0 -or $route.Count -eq 0) { throw 'Gateway interface or external route unavailable' }
 $indexes += $tun | ForEach-Object { [PSCustomObject]@{Index=$_.InterfaceIndex; Family=$r.Family} }
 $indexes += $route | ForEach-Object { [PSCustomObject]@{Index=$_.InterfaceIndex; Family=$r.Family} }
}
foreach ($i in ($indexes | Sort-Object Index,Family -Unique)) {
 Set-NetIPInterface -InterfaceIndex $i.Index -AddressFamily $i.Family -Forwarding Enabled -PolicyStore ActiveStore -ErrorAction Stop
}
Get-NetFirewallRule -PolicyStore PersistentStore | Where-Object { $_.Group -eq $group } | Remove-NetFirewallRule -ErrorAction Stop
$index = 0
foreach ($r in $rules) {
 foreach ($direction in @('Inbound','Outbound')) {
  New-NetFirewallRule -Name "$group-$index-$direction" -DisplayName "$group-$index-$direction" -Group $group -Direction $direction -Action Allow -Enabled True -Profile Any -InterfaceAlias $r.Interface -LocalAddress $r.Overlay -RemoteAddress $r.Subnet -Protocol Any -ErrorAction Stop | Out-Null
 }
 $index++
}
`, group, strings.ReplaceAll(string(raw), "'", "''"))
	// UTF-16 EncodedCommand prevents command-line quoting from interpreting data.
	encoded := utf16.Encode([]rune("Invoke-Expression ([Console]::In.ReadToEnd())"))
	bytes := make([]byte, 2*len(encoded))
	for i, u := range encoded {
		binary.LittleEndian.PutUint16(bytes[2*i:], u)
	}
	_, err = command(ctx, "powershell.exe", script, "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(bytes))
	return err
}
