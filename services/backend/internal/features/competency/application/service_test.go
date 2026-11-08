package application

import (
	"testing"

	"github.com/AskarKasimov/ai-tutor/services/backend/internal/entities/user"
	"github.com/AskarKasimov/ai-tutor/services/backend/internal/shared/fault"
)

func TestOnlyAdminMayImportAMap(t *testing.T) {
	service := New(nil)
	if err := service.Authorize(user.User{Role: user.Admin}); err != nil {
		t.Fatalf("admin: %v", err)
	}
	err := service.Authorize(user.User{Role: user.Student})
	if f, ok := err.(*fault.Error); !ok || f.Code != "FORBIDDEN" {
		t.Fatalf("student: %v", err)
	}
}
