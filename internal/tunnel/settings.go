package tunnel

import (
	"errors"
	"fmt"
)

// Each setting is a single atomic OS update. This lets a multi-family MTU
// update honor configOperations.setMTU's all-or-restore contract.
type settingChange struct{ apply, restore func() error }

func changeSettings(changes []settingChange) error {
	for i, change := range changes {
		if err := change.apply(); err != nil {
			var rollback error
			for j := i - 1; j >= 0; j-- {
				rollback = errors.Join(rollback, changes[j].restore())
			}
			if rollback != nil {
				return errors.Join(err, fmt.Errorf("%w: restore interface settings: %w", ErrUnavailable, rollback))
			}
			return err
		}
	}
	return nil
}
