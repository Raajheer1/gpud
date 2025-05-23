package server

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/leptonai/gpud/pkg/config"
)

func TestServerErrorForEmptyConfig(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s, err := New(ctx, &config.Config{}, "", "", nil)
	require.Nil(t, s)
	require.NotNil(t, err)
}

func TestServerConfigValidation(t *testing.T) {
	tests := []struct {
		name        string
		config      *config.Config
		expectedErr string
	}{
		{
			name:        "empty config",
			config:      &config.Config{},
			expectedErr: "address is required",
		},
		{
			name: "retention period too short",
			config: &config.Config{
				Address:         "localhost:8080",
				RetentionPeriod: metav1.Duration{Duration: 30 * time.Second},
			},
			expectedErr: "retention_period must be at least 1 minute",
		},
		{
			name: "invalid auto update exit code",
			config: &config.Config{
				Address:            "localhost:8080",
				RetentionPeriod:    metav1.Duration{Duration: time.Hour},
				EnableAutoUpdate:   false,
				AutoUpdateExitCode: 1,
			},
			expectedErr: "auto_update_exit_code is only valid when auto_update is enabled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			s, err := New(ctx, tt.config, "", "", nil)
			require.Nil(t, s)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.expectedErr)
		})
	}
}
