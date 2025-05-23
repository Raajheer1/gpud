package processes

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/NVIDIA/go-nvlib/pkg/nvlib/device"
	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"github.com/NVIDIA/go-nvml/pkg/nvml/mock"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/leptonai/gpud/api/v1"
	"github.com/leptonai/gpud/components"
	nvidianvml "github.com/leptonai/gpud/pkg/nvidia-query/nvml"
	nvmllib "github.com/leptonai/gpud/pkg/nvidia-query/nvml/lib"
	"github.com/leptonai/gpud/pkg/nvidia-query/nvml/testutil"
)

// Override the metricRunningProcesses for testing
var _ = prometheus.Register(metricRunningProcesses) // This will fail silently if metrics are already registered

// mockNVMLInstance is a mock implementation of nvidianvml.InstanceV2
type mockNVMLInstance struct {
	devicesFunc func() map[string]device.Device
	nvmlExists  bool
}

func (m *mockNVMLInstance) Devices() map[string]device.Device {
	if m.devicesFunc != nil {
		return m.devicesFunc()
	}
	return nil
}

// Override other InstanceV2 methods to return empty values
func (m *mockNVMLInstance) NVMLExists() bool         { return m.nvmlExists }
func (m *mockNVMLInstance) Library() nvmllib.Library { return nil }
func (m *mockNVMLInstance) ProductName() string      { return "Test GPU" }
func (m *mockNVMLInstance) GetMemoryErrorManagementCapabilities() nvidianvml.MemoryErrorManagementCapabilities {
	return nvidianvml.MemoryErrorManagementCapabilities{}
}
func (m *mockNVMLInstance) Shutdown() error { return nil }

func createMockDevice(uuid string, runningProcs []nvml.ProcessInfo) device.Device {
	mockDevice := &mock.Device{
		GetComputeRunningProcessesFunc: func() ([]nvml.ProcessInfo, nvml.Return) {
			return runningProcs, nvml.SUCCESS
		},
		GetUUIDFunc: func() (string, nvml.Return) {
			return uuid, nvml.SUCCESS
		},
		GetProcessUtilizationFunc: func(pid uint64) ([]nvml.ProcessUtilizationSample, nvml.Return) {
			return []nvml.ProcessUtilizationSample{
				{
					Pid:       uint32(pid),
					TimeStamp: 123456789,
					SmUtil:    50,
					MemUtil:   30,
					EncUtil:   0,
					DecUtil:   0,
				},
			}, nvml.SUCCESS
		},
	}

	return testutil.NewMockDevice(mockDevice, "test-arch", "test-brand", "test-cuda", "test-pci")
}

func TestNew(t *testing.T) {
	ctx := context.Background()
	mockInstance := &mockNVMLInstance{nvmlExists: true}

	// Create a mock GPUdInstance with the required fields
	gpudInstance := &components.GPUdInstance{
		RootCtx:      ctx,
		NVMLInstance: mockInstance,
	}

	comp, err := New(gpudInstance)

	assert.NoError(t, err)
	assert.NotNil(t, comp)
	assert.Equal(t, Name, comp.Name())
}

func TestComponentName(t *testing.T) {
	ctx := context.Background()
	mockInstance := &mockNVMLInstance{nvmlExists: true}

	// Create a mock GPUdInstance with the required fields
	gpudInstance := &components.GPUdInstance{
		RootCtx:      ctx,
		NVMLInstance: mockInstance,
	}

	comp, err := New(gpudInstance)

	assert.NoError(t, err)
	assert.Equal(t, Name, comp.Name())
}

func TestStartAndClose(t *testing.T) {
	ctx := context.Background()
	mockInstance := &mockNVMLInstance{nvmlExists: true}

	// Create a mock GPUdInstance with the required fields
	gpudInstance := &components.GPUdInstance{
		RootCtx:      ctx,
		NVMLInstance: mockInstance,
	}

	comp, err := New(gpudInstance)
	assert.NoError(t, err)

	// Test Start method
	err = comp.Start()
	assert.NoError(t, err)

	// Test Close method
	err = comp.Close()
	assert.NoError(t, err)
}

func TestEvents(t *testing.T) {
	ctx := context.Background()
	mockInstance := &mockNVMLInstance{nvmlExists: true}

	// Create a mock GPUdInstance with the required fields
	gpudInstance := &components.GPUdInstance{
		RootCtx:      ctx,
		NVMLInstance: mockInstance,
	}

	comp, err := New(gpudInstance)
	assert.NoError(t, err)

	events, err := comp.Events(ctx, time.Now())
	assert.NoError(t, err)
	assert.Empty(t, events)
}

func TestCheck(t *testing.T) {
	ctx := context.Background()

	// Create mock devices with running processes
	mockDev1 := createMockDevice("gpu-uuid-1", []nvml.ProcessInfo{
		{Pid: 1234, UsedGpuMemory: 100000000},
	})

	mockDevices := map[string]device.Device{
		"gpu-uuid-1": mockDev1,
	}

	mockInstance := &mockNVMLInstance{
		nvmlExists: true,
		devicesFunc: func() map[string]device.Device {
			return mockDevices
		},
	}

	// Create a mock GPUdInstance with the required fields
	gpudInstance := &components.GPUdInstance{
		RootCtx:      ctx,
		NVMLInstance: mockInstance,
	}

	comp, err := New(gpudInstance)
	assert.NoError(t, err)

	// Cast to component to access internal Check method
	c := comp.(*component)

	// Set the getProcessesFunc for testing
	c.getProcessesFunc = func(uuid string, dev device.Device) (nvidianvml.Processes, error) {
		return nvidianvml.Processes{
			UUID: uuid,
			RunningProcesses: []nvidianvml.Process{
				{
					PID:                1234,
					GPUUsedMemoryBytes: 100000000,
				},
			},
		}, nil
	}

	// Run Check
	result := c.Check()

	// Verify result is correct
	data, ok := result.(*Data)
	require.True(t, ok)
	assert.Equal(t, apiv1.HealthStateTypeHealthy, data.health)
	assert.Equal(t, 1, len(data.Processes))
}

func TestCheckError(t *testing.T) {
	ctx := context.Background()

	// Create mock devices with no processes
	mockDev1 := createMockDevice("gpu-uuid-1", nil)

	mockDevices := map[string]device.Device{
		"gpu-uuid-1": mockDev1,
	}

	mockInstance := &mockNVMLInstance{
		nvmlExists: true,
		devicesFunc: func() map[string]device.Device {
			return mockDevices
		},
	}

	// Create a mock GPUdInstance with the required fields
	gpudInstance := &components.GPUdInstance{
		RootCtx:      ctx,
		NVMLInstance: mockInstance,
	}

	comp, err := New(gpudInstance)
	assert.NoError(t, err)

	// Cast to component to access internal methods
	c := comp.(*component)

	// Create a getProcessesFunc that returns an error
	testErr := errors.New("test error")
	c.getProcessesFunc = func(uuid string, dev device.Device) (nvidianvml.Processes, error) {
		return nvidianvml.Processes{}, testErr
	}

	// Run Check
	result := c.Check()

	// Verify result is correct
	data, ok := result.(*Data)
	require.True(t, ok)
	assert.Equal(t, apiv1.HealthStateTypeUnhealthy, data.health)
	assert.Equal(t, testErr, data.err)
}

func TestLastHealthStates(t *testing.T) {
	ctx := context.Background()
	mockInstance := &mockNVMLInstance{nvmlExists: true}

	// Create a mock GPUdInstance with the required fields
	gpudInstance := &components.GPUdInstance{
		RootCtx:      ctx,
		NVMLInstance: mockInstance,
	}

	comp, err := New(gpudInstance)
	assert.NoError(t, err)

	// Cast to component to access internal lastData
	c := comp.(*component)

	// At this point, lastData should be nil
	states := c.LastHealthStates()
	assert.Equal(t, 1, len(states))

	// Default values for nil data
	assert.Equal(t, Name, states[0].Name)
	assert.Equal(t, apiv1.HealthStateTypeHealthy, states[0].Health)
	assert.Equal(t, "no data yet", states[0].Reason)
}

func TestDataGetLastHealthStates(t *testing.T) {
	// Test healthy data
	healthyData := &Data{
		Processes: []nvidianvml.Processes{
			{
				UUID: "gpu-uuid-1",
				RunningProcesses: []nvidianvml.Process{
					{PID: 1234},
				},
			},
		},
		health: apiv1.HealthStateTypeHealthy,
		reason: "all GPUs healthy",
	}

	states := healthyData.getLastHealthStates()
	assert.Equal(t, 1, len(states))
	assert.Equal(t, apiv1.HealthStateTypeHealthy, states[0].Health)

	// Test unhealthy data
	testErr := errors.New("test error")
	unhealthyData := &Data{
		err:    testErr,
		health: apiv1.HealthStateTypeUnhealthy,
		reason: "GPU issue detected",
	}

	states = unhealthyData.getLastHealthStates()
	assert.Equal(t, 1, len(states))
	assert.Equal(t, apiv1.HealthStateTypeUnhealthy, states[0].Health)
	assert.Equal(t, testErr.Error(), states[0].Error)
}

func TestDataGetError(t *testing.T) {
	// Test nil data
	var nilData *Data
	assert.Equal(t, "", nilData.getError())

	// Test nil error
	noErrorData := &Data{}
	assert.Equal(t, "", noErrorData.getError())

	// Test with error
	testErr := errors.New("test error")
	errorData := &Data{err: testErr}
	assert.Equal(t, testErr.Error(), errorData.getError())
}

// Test Data.String() method
func TestDataString(t *testing.T) {
	// Test nil data
	var nilData *Data
	assert.Equal(t, "", nilData.String())

	// Test empty data
	emptyData := &Data{}
	assert.Equal(t, "no data", emptyData.String())

	// Test data with processes
	dataWithProcesses := &Data{
		Processes: []nvidianvml.Processes{
			{
				UUID: "gpu-uuid-1",
				RunningProcesses: []nvidianvml.Process{
					{PID: 1234},
					{PID: 5678},
				},
			},
		},
	}

	result := dataWithProcesses.String()
	assert.Contains(t, result, "gpu-uuid-1")
	assert.Contains(t, result, "2") // Number of processes
}

// Test Data.Summary() method
func TestDataSummary(t *testing.T) {
	// Test nil data
	var nilData *Data
	assert.Equal(t, "", nilData.Summary())

	// Test data with reason
	dataWithReason := &Data{
		reason: "test reason",
	}
	assert.Equal(t, "test reason", dataWithReason.Summary())
}

// Test Data.HealthState() method
func TestDataHealthState(t *testing.T) {
	// Test nil data
	var nilData *Data
	assert.Equal(t, apiv1.HealthStateType(""), nilData.HealthState())

	// Test data with health state
	dataWithHealth := &Data{
		health: apiv1.HealthStateTypeHealthy,
	}
	assert.Equal(t, apiv1.HealthStateTypeHealthy, dataWithHealth.HealthState())

	// Test data with unhealthy state
	dataUnhealthy := &Data{
		health: apiv1.HealthStateTypeUnhealthy,
	}
	assert.Equal(t, apiv1.HealthStateTypeUnhealthy, dataUnhealthy.HealthState())
}

// Test Check edge cases
func TestCheckEdgeCases(t *testing.T) {
	ctx := context.Background()

	t.Run("nil NVML instance", func(t *testing.T) {
		gpudInstance := &components.GPUdInstance{
			RootCtx:      ctx,
			NVMLInstance: nil,
		}

		comp, err := New(gpudInstance)
		assert.NoError(t, err)

		c := comp.(*component)
		result := c.Check()

		data, ok := result.(*Data)
		require.True(t, ok)
		assert.Equal(t, apiv1.HealthStateTypeHealthy, data.health)
		assert.Equal(t, "NVIDIA NVML instance is nil", data.reason)
	})

	t.Run("NVML not loaded", func(t *testing.T) {
		mockInstance := &mockNVMLInstance{
			nvmlExists: false,
		}

		gpudInstance := &components.GPUdInstance{
			RootCtx:      ctx,
			NVMLInstance: mockInstance,
		}

		comp, err := New(gpudInstance)
		assert.NoError(t, err)

		c := comp.(*component)
		result := c.Check()

		data, ok := result.(*Data)
		require.True(t, ok)
		assert.Equal(t, apiv1.HealthStateTypeHealthy, data.health)
		assert.Equal(t, "NVIDIA NVML is not loaded", data.reason)
	})

	t.Run("multiple devices", func(t *testing.T) {
		// Create mock devices with running processes
		mockDev1 := createMockDevice("gpu-uuid-1", []nvml.ProcessInfo{
			{Pid: 1234, UsedGpuMemory: 100000000},
		})
		mockDev2 := createMockDevice("gpu-uuid-2", []nvml.ProcessInfo{
			{Pid: 5678, UsedGpuMemory: 200000000},
			{Pid: 9012, UsedGpuMemory: 300000000},
		})

		mockDevices := map[string]device.Device{
			"gpu-uuid-1": mockDev1,
			"gpu-uuid-2": mockDev2,
		}

		mockInstance := &mockNVMLInstance{
			nvmlExists: true,
			devicesFunc: func() map[string]device.Device {
				return mockDevices
			},
		}

		gpudInstance := &components.GPUdInstance{
			RootCtx:      ctx,
			NVMLInstance: mockInstance,
		}

		comp, err := New(gpudInstance)
		assert.NoError(t, err)

		c := comp.(*component)

		// Set up the getProcessesFunc to match device UUID
		c.getProcessesFunc = func(uuid string, dev device.Device) (nvidianvml.Processes, error) {
			if uuid == "gpu-uuid-1" {
				return nvidianvml.Processes{
					UUID: uuid,
					RunningProcesses: []nvidianvml.Process{
						{PID: 1234, GPUUsedMemoryBytes: 100000000},
					},
				}, nil
			} else {
				return nvidianvml.Processes{
					UUID: uuid,
					RunningProcesses: []nvidianvml.Process{
						{PID: 5678, GPUUsedMemoryBytes: 200000000},
						{PID: 9012, GPUUsedMemoryBytes: 300000000},
					},
				}, nil
			}
		}

		result := c.Check()

		data, ok := result.(*Data)
		require.True(t, ok)
		assert.Equal(t, apiv1.HealthStateTypeHealthy, data.health)
		assert.Equal(t, 2, len(data.Processes))
		assert.Equal(t, "all 2 GPU(s) were checked, no process issue found", data.reason)
	})
}
