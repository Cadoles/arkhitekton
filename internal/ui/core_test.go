package ui

import (
	"testing"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/auth"
)

// The core's resources compose on their own, against the permissions roles actually grant.
func TestTheCoresInterfaceComposes(t *testing.T) {
	core, err := CoreResources()
	if err != nil {
		t.Fatal(err)
	}
	known := Options{KnownPermission: func(permission string) bool { return auth.Known(auth.Permission(permission)) }}
	if _, refused := Compose(core, nil, known); len(refused) > 0 {
		t.Fatal(refused)
	}
}
