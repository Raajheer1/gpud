// Package power tracks the NVIDIA per-GPU power usage.
package power

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NVIDIA/go-nvlib/pkg/nvlib/device"
	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"github.com/NVIDIA/go-nvml/pkg/nvml/mock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/leptonai/gpud/api/v1"
	"github.com/leptonai/gpud/components"
	nvidianvml "github.com/leptonai/gpud/pkg/nvidia-query/nvml"
	nvml_lib "github.com/leptonai/gpud/pkg/nvidia-query/nvml/lib"
	"github.com/leptonai/gpud/pkg/nvidia-query/nvml/testutil"
)

// MockPowerComponent creates a component with mocked functions for testing
func MockPowerComponent(
	ctx context.Context,
	mockNvmlInstance *mockNvmlInstance,
	getPowerFunc func(uuid string, dev device.Device) (nvidianvml.Power, error),
) components.Component {
	cctx, cancel := context.WithCancel(ctx)
	return &component{
		ctx:          cctx,
		cancel:       cancel,
		nvmlInstance: mockNvmlInstance,
		getPowerFunc: getPowerFunc,
	}
}

// mockNvmlInstance implements InstanceV2 interface for testing
type mockNvmlInstance struct {
	devices map[string]device.Device
}

func (m *mockNvmlInstance) NVMLExists() bool {
	return true
}

func (m *mockNvmlInstance) Library() nvml_lib.Library {
	return nil
}

func (m *mockNvmlInstance) Devices() map[string]device.Device {
	return m.devices
}

func (m *mockNvmlInstance) ProductName() string {
	return "Test GPU"
}

func (m *mockNvmlInstance) GetMemoryErrorManagementCapabilities() nvidianvml.MemoryErrorManagementCapabilities {
	return nvidianvml.MemoryErrorManagementCapabilities{}
}

func (m *mockNvmlInstance) Shutdown() error {
	return nil
}

func TestNew(t *testing.T) {
	ctx := context.Background()
	mockInstance := &mockNvmlInstance{devices: make(map[string]device.Device)}

	// Create a mock GPUdInstance
	gpudInstance := &components.GPUdInstance{
		RootCtx:      ctx,
		NVMLInstance: mockInstance,
	}

	c, err := New(gpudInstance)
	assert.NoError(t, err)
	assert.NotNil(t, c, "New should return a non-nil component")
	assert.Equal(t, Name, c.Name(), "Component name should match")

	// Type assertion to access internal fields
	tc, ok := c.(*component)
	require.True(t, ok, "Component should be of type *component")

	assert.NotNil(t, tc.ctx, "Context should be set")
	assert.NotNil(t, tc.cancel, "Cancel function should be set")
	assert.NotNil(t, tc.nvmlInstance, "nvmlInstance should be set")
	assert.NotNil(t, tc.getPowerFunc, "getPowerFunc should be set")
}

func TestName(t *testing.T) {
	ctx := context.Background()
	c := MockPowerComponent(ctx, nil, nil)
	assert.Equal(t, Name, c.Name(), "Component name should match")
}

func TestCheckOnce_Success(t *testing.T) {
	ctx := context.Background()

	uuid := "gpu-uuid-123"
	mockDeviceObj := &mock.Device{
		GetUUIDFunc: func() (string, nvml.Return) {
			return uuid, nvml.SUCCESS
		},
	}
	mockDev := testutil.NewMockDevice(mockDeviceObj, "test-arch", "test-brand", "test-cuda", "test-pci")

	devs := map[string]device.Device{
		uuid: mockDev,
	}

	mockNvml := &mockNvmlInstance{
		devices: devs,
	}

	power := nvidianvml.Power{
		UUID:                             uuid,
		UsageMilliWatts:                  150000,  // 150W
		EnforcedLimitMilliWatts:          250000,  // 250W
		ManagementLimitMilliWatts:        300000,  // 300W
		UsedPercent:                      "60.00", // Important: Must be set for GetUsedPercent
		GetPowerUsageSupported:           true,
		GetPowerLimitSupported:           true,
		GetPowerManagementLimitSupported: true,
	}

	getPowerFunc := func(uuid string, dev device.Device) (nvidianvml.Power, error) {
		return power, nil
	}

	component := MockPowerComponent(ctx, mockNvml, getPowerFunc).(*component)
	result := component.Check()

	// Cast the result to *Data
	lastData := result.(*Data)

	require.NotNil(t, lastData, "lastData should not be nil")
	assert.Equal(t, apiv1.HealthStateTypeHealthy, lastData.health, "data should be marked healthy")
	assert.Equal(t, "all 1 GPU(s) were checked, no power issue found", lastData.reason)
	assert.Len(t, lastData.Powers, 1)
	assert.Equal(t, power, lastData.Powers[0])
}

func TestCheckOnce_PowerError(t *testing.T) {
	ctx := context.Background()

	uuid := "gpu-uuid-123"
	mockDeviceObj := &mock.Device{
		GetUUIDFunc: func() (string, nvml.Return) {
			return uuid, nvml.SUCCESS
		},
	}
	mockDev := testutil.NewMockDevice(mockDeviceObj, "test-arch", "test-brand", "test-cuda", "test-pci")

	devs := map[string]device.Device{
		uuid: mockDev,
	}

	mockNvml := &mockNvmlInstance{
		devices: devs,
	}

	errExpected := errors.New("power error")
	getPowerFunc := func(uuid string, dev device.Device) (nvidianvml.Power, error) {
		return nvidianvml.Power{}, errExpected
	}

	component := MockPowerComponent(ctx, mockNvml, getPowerFunc).(*component)
	result := component.Check()

	// Cast the result to *Data
	lastData := result.(*Data)

	require.NotNil(t, lastData, "lastData should not be nil")
	assert.Equal(t, apiv1.HealthStateTypeUnhealthy, lastData.health, "data should be marked unhealthy")
	assert.Equal(t, errExpected, lastData.err)
	assert.Equal(t, "error getting power for device gpu-uuid-123", lastData.reason)
}

func TestCheckOnce_NoDevices(t *testing.T) {
	ctx := context.Background()

	mockNvml := &mockNvmlInstance{
		devices: map[string]device.Device{}, // Empty map
	}

	component := MockPowerComponent(ctx, mockNvml, nil).(*component)
	result := component.Check()

	// Cast the result to *Data
	lastData := result.(*Data)

	require.NotNil(t, lastData, "lastData should not be nil")
	assert.Equal(t, apiv1.HealthStateTypeHealthy, lastData.health, "data should be marked healthy")
	assert.Equal(t, "all 0 GPU(s) were checked, no power issue found", lastData.reason)
	assert.Empty(t, lastData.Powers)
}

func TestCheckOnce_GetUsedPercentError(t *testing.T) {
	ctx := context.Background()

	uuid := "gpu-uuid-123"
	mockDeviceObj := &mock.Device{
		GetUUIDFunc: func() (string, nvml.Return) {
			return uuid, nvml.SUCCESS
		},
	}
	mockDev := testutil.NewMockDevice(mockDeviceObj, "test-arch", "test-brand", "test-cuda", "test-pci")

	devs := map[string]device.Device{
		uuid: mockDev,
	}

	mockNvml := &mockNvmlInstance{
		devices: devs,
	}

	// Create power data with invalid UsedPercent format
	invalidPower := nvidianvml.Power{
		UUID:                             uuid,
		UsageMilliWatts:                  150000,
		EnforcedLimitMilliWatts:          250000,
		ManagementLimitMilliWatts:        300000,
		UsedPercent:                      "invalid", // Will cause ParseFloat to fail
		GetPowerUsageSupported:           true,
		GetPowerLimitSupported:           true,
		GetPowerManagementLimitSupported: true,
	}

	getPowerFunc := func(uuid string, dev device.Device) (nvidianvml.Power, error) {
		return invalidPower, nil
	}

	component := MockPowerComponent(ctx, mockNvml, getPowerFunc).(*component)
	result := component.Check()

	// Cast the result to *Data
	lastData := result.(*Data)

	require.NotNil(t, lastData, "lastData should not be nil")
	assert.Equal(t, apiv1.HealthStateTypeUnhealthy, lastData.health, "data should be marked unhealthy")
	assert.NotNil(t, lastData.err)
	assert.Equal(t, "error getting used percent for device gpu-uuid-123", lastData.reason)
}

func TestStates_WithData(t *testing.T) {
	ctx := context.Background()
	component := MockPowerComponent(ctx, nil, nil).(*component)

	// Set test data
	component.lastMu.Lock()
	component.lastData = &Data{
		Powers: []nvidianvml.Power{
			{
				UUID:                             "gpu-uuid-123",
				UsageMilliWatts:                  150000, // 150W
				EnforcedLimitMilliWatts:          250000, // 250W
				ManagementLimitMilliWatts:        300000, // 300W
				UsedPercent:                      "60.00",
				GetPowerUsageSupported:           true,
				GetPowerLimitSupported:           true,
				GetPowerManagementLimitSupported: true,
			},
		},
		health: apiv1.HealthStateTypeHealthy,
		reason: "all 1 GPU(s) were checked, no power issue found",
	}
	component.lastMu.Unlock()

	// Get states
	states := component.LastHealthStates()
	assert.Len(t, states, 1)

	state := states[0]
	assert.Equal(t, Name, state.Name)
	assert.Equal(t, apiv1.HealthStateTypeHealthy, state.Health)
	assert.Equal(t, "all 1 GPU(s) were checked, no power issue found", state.Reason)
	assert.Contains(t, state.DeprecatedExtraInfo["data"], "gpu-uuid-123")
}

func TestStates_WithError(t *testing.T) {
	ctx := context.Background()
	component := MockPowerComponent(ctx, nil, nil).(*component)

	// Set test data with error
	component.lastMu.Lock()
	component.lastData = &Data{
		err:    errors.New("test power error"),
		health: apiv1.HealthStateTypeUnhealthy,
		reason: "error getting power for device gpu-uuid-123",
	}
	component.lastMu.Unlock()

	// Get states
	states := component.LastHealthStates()
	assert.Len(t, states, 1)

	state := states[0]
	assert.Equal(t, Name, state.Name)
	assert.Equal(t, apiv1.HealthStateTypeUnhealthy, state.Health)
	assert.Equal(t, "error getting power for device gpu-uuid-123", state.Reason)
	assert.Equal(t, "test power error", state.Error)
}

func TestStates_NoData(t *testing.T) {
	ctx := context.Background()
	component := MockPowerComponent(ctx, nil, nil).(*component)

	// Don't set any data

	// Get states
	states := component.LastHealthStates()
	assert.Len(t, states, 1)

	state := states[0]
	assert.Equal(t, Name, state.Name)
	assert.Equal(t, apiv1.HealthStateTypeHealthy, state.Health)
	assert.Equal(t, "no data yet", state.Reason)
}

func TestEvents(t *testing.T) {
	ctx := context.Background()
	component := MockPowerComponent(ctx, nil, nil)

	events, err := component.Events(ctx, time.Now())
	assert.NoError(t, err)
	assert.Empty(t, events)
}

func TestStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create mock functions that count calls
	callCount := &atomic.Int32{}
	mockNvml := &mockNvmlInstance{
		devices: map[string]device.Device{
			"gpu-uuid-123": testutil.NewMockDevice(nil, "test-arch", "test-brand", "test-cuda", "test-pci"),
		},
	}

	component := MockPowerComponent(ctx, mockNvml, func(uuid string, dev device.Device) (nvidianvml.Power, error) {
		callCount.Add(1)
		return nvidianvml.Power{}, nil
	})

	// Start should be non-blocking
	err := component.Start()
	assert.NoError(t, err)

	// Give the goroutine time to execute Check at least once
	time.Sleep(time.Second)

	// Verify Check was called
	assert.GreaterOrEqual(t, callCount.Load(), int32(1), "Check should have been called at least once")
}

func TestClose(t *testing.T) {
	ctx := context.Background()
	component := MockPowerComponent(ctx, nil, nil).(*component)

	err := component.Close()
	assert.NoError(t, err)

	// Check that context is canceled
	select {
	case <-component.ctx.Done():
		// Context is properly canceled
	default:
		t.Fatal("component context was not canceled on Close")
	}
}

func TestData_GetError(t *testing.T) {
	tests := []struct {
		name     string
		data     *Data
		expected string
	}{
		{
			name:     "nil data",
			data:     nil,
			expected: "",
		},
		{
			name: "with error",
			data: &Data{
				err: errors.New("test error"),
			},
			expected: "test error",
		},
		{
			name: "no error",
			data: &Data{
				health: apiv1.HealthStateTypeHealthy,
				reason: "all good",
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.data.getError()
			assert.Equal(t, tt.expected, got)
		})
	}
}
