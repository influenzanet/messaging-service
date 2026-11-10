package globaldb

import (
	"testing"
)

func TestDbInterfaceMethods(t *testing.T) {
	t.Run("Check fetching instances", func(t *testing.T) {
		instances, err := testDBService.GetAllInstances()
		if err != nil {
			t.Error(err.Error())
			return
		}
		if err != nil {
			t.Error(err.Error())
			return
		}
		if len(instances) != 2 {
			t.Errorf("unexpected number of instances: %d", len(instances))
		}
	})
}
