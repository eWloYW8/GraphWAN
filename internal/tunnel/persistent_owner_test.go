package tunnel

import (
	"errors"
	"net"
	"slices"
	"testing"
)

func TestPersistentTunOwnershipPublication(t *testing.T) {
	const name, token = "tun300", "0123456789abcdef0123456789abcdef"
	lookupError := errors.New("interface enumeration failed")
	markError := errors.New("description write failed")
	cleanupError := errors.New("interface destruction failed")
	for _, test := range []struct {
		name       string
		iface      *net.Interface
		lookupErr  error
		markErr    error
		cleanupErr error
		wantCalls  []string
	}{
		{"published", &net.Interface{Name: name, Index: 7}, nil, nil, nil, []string{"lookup", "mark"}},
		{"failed lookup preserves unknown interface", nil, lookupError, nil, nil, []string{"lookup"}},
		{"missing identity", nil, nil, nil, nil, []string{"lookup"}},
		{"wrong name", &net.Interface{Name: "tun301", Index: 7}, nil, nil, nil, []string{"lookup"}},
		{"invalid index", &net.Interface{Name: name}, nil, nil, nil, []string{"lookup"}},
		{"failed marker cleans verified index", &net.Interface{Name: name, Index: 7}, nil, markError, nil, []string{"lookup", "mark", "destroy"}},
		{"cleanup error retained", &net.Interface{Name: name, Index: 7}, nil, markError, cleanupError, []string{"lookup", "mark", "destroy"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			index, err := publishTunOwnership(name, token, func(got string) (*net.Interface, error) {
				calls = append(calls, "lookup")
				if got != name {
					t.Fatal("looked up another interface")
				}
				return test.iface, test.lookupErr
			}, func(got, marker string) (string, error) {
				calls = append(calls, "mark")
				if got != name || marker != "graphwan:"+token+":7" {
					t.Fatal("ownership marker lost its token/index binding", got, marker)
				}
				return marker, test.markErr
			}, func(got string, index int) error {
				calls = append(calls, "destroy")
				if got != name || index != 7 {
					t.Fatal("cleanup lacked the original interface identity")
				}
				return test.cleanupErr
			})
			if !slices.Equal(calls, test.wantCalls) {
				t.Fatalf("calls=%v want=%v", calls, test.wantCalls)
			}
			if test.name == "published" {
				if err != nil || index != 7 {
					t.Fatal(index, err)
				}
				return
			}
			if err == nil || index != 0 {
				t.Fatal("failed publication returned an owned interface", index, err)
			}
			for _, cause := range []error{test.lookupErr, test.markErr, test.cleanupErr} {
				if cause != nil && !errors.Is(err, cause) {
					t.Fatal("failure lost its cause", cause, err)
				}
			}
		})
	}
}
