package contracts

import (
	"slices"
	"testing"
)

func TestRedisContract_Registered(t *testing.T) {
	c, ok := Lookup("redis")
	if !ok {
		t.Fatal("redis contract not registered")
	}
	for _, want := range []string{"REDIS_URL", "REDIS_HOST", "REDIS_PORT", "REDIS_PASSWORD"} {
		if !c.HasKey(want) {
			t.Errorf("redis missing required key %q (got %v)", want, c.RequiredKeys)
		}
	}
}

func TestPostgresContract_Registered(t *testing.T) {
	c, ok := Lookup("postgres")
	if !ok {
		t.Fatal("postgres contract not registered")
	}
	for _, want := range []string{"DATABASE_URL", "DATABASE_HOST", "DATABASE_PORT", "DATABASE_NAME", "DATABASE_USER", "DATABASE_PASSWORD"} {
		if !c.HasKey(want) {
			t.Errorf("postgres missing required key %q (got %v)", want, c.RequiredKeys)
		}
	}
}

func TestUnknownTypeNotFound(t *testing.T) {
	if _, ok := Lookup("kafka"); ok {
		t.Error("kafka should not be registered yet")
	}
}

func TestTypes_Sorted(t *testing.T) {
	got := Types()
	want := []string{"postgres", "redis"}
	if !slices.Equal(got, want) {
		t.Errorf("Types() = %v, want %v (sorted)", got, want)
	}
}

func TestRegister_PanicsOnDuplicate(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on duplicate Register, got none")
		}
	}()
	Register(Contract{Type: "redis", RequiredKeys: []string{"X"}})
}
