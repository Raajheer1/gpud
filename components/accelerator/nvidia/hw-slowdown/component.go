// Package hwslowdown monitors NVIDIA GPU hardware clock events of all GPUs, such as HW Slowdown events.
package hwslowdown

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/go-nvlib/pkg/nvlib/device"
	"github.com/olekukonko/tablewriter"
	"github.com/prometheus/client_golang/prometheus"

	apiv1 "github.com/leptonai/gpud/api/v1"
	"github.com/leptonai/gpud/components"
	"github.com/leptonai/gpud/pkg/eventstore"
	"github.com/leptonai/gpud/pkg/log"
	pkgmetrics "github.com/leptonai/gpud/pkg/metrics"
	nvidianvml "github.com/leptonai/gpud/pkg/nvidia-query/nvml"
)

const (
	Name = "accelerator-nvidia-hw-slowdown"

	// DefaultStateHWSlowdownEvaluationWindow is the window to evaluate the HW slowdown state.
	DefaultStateHWSlowdownEvaluationWindow = 10 * time.Minute

	// DefaultStateHWSlowdownEventsThresholdFrequencyPerMinute is the threshold frequency of the HW slowdown events per minute.
	// If the evaluation window is 10 minutes and for the last 10-minute, 6 events are found, the state is considered unhealthy, where the ratio is 0.6 = 6 / 10.
	// This is to avoid false positives when the HW slowdown events are rare.
	DefaultStateHWSlowdownEventsThresholdFrequencyPerMinute = 0.6
)

var _ components.Component = &component{}

type component struct {
	ctx    context.Context
	cancel context.CancelFunc

	nvmlInstance                  nvidianvml.InstanceV2
	getClockEventsSupportedFunc   func(dev device.Device) (bool, error)
	getClockEventsFunc            func(uuid string, dev device.Device) (nvidianvml.ClockEvents, error)
	getSystemDriverVersionFunc    func() (string, error)
	parseDriverVersionFunc        func(driverVersion string) (int, int, int, error)
	checkClockEventsSupportedFunc func(major int) bool

	eventBucket eventstore.Bucket

	evaluationWindow time.Duration
	threshold        float64

	lastMu   sync.RWMutex
	lastData *Data
}

func New(gpudInstance *components.GPUdInstance) (components.Component, error) {
	cctx, ccancel := context.WithCancel(gpudInstance.RootCtx)
	c := &component{
		ctx:    cctx,
		cancel: ccancel,

		nvmlInstance:                gpudInstance.NVMLInstance,
		getClockEventsSupportedFunc: nvidianvml.ClockEventsSupportedByDevice,
		getClockEventsFunc:          nvidianvml.GetClockEvents,

		evaluationWindow: DefaultStateHWSlowdownEvaluationWindow,
		threshold:        DefaultStateHWSlowdownEventsThresholdFrequencyPerMinute,
	}

	if gpudInstance.NVMLInstance != nil && gpudInstance.NVMLInstance.NVMLExists() {
		c.getSystemDriverVersionFunc = func() (string, error) {
			return nvidianvml.GetSystemDriverVersion(gpudInstance.NVMLInstance.Library().NVML())
		}
		c.parseDriverVersionFunc = nvidianvml.ParseDriverVersion
		c.checkClockEventsSupportedFunc = nvidianvml.ClockEventsSupportedVersion
	}

	if gpudInstance.EventStore != nil && runtime.GOOS == "linux" {
		var err error
		c.eventBucket, err = gpudInstance.EventStore.Bucket(Name)
		if err != nil {
			ccancel()
			return nil, err
		}
	}

	return c, nil
}

func (c *component) Name() string { return Name }

func (c *component) Start() error {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()

		for {
			_ = c.Check()

			select {
			case <-c.ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return nil
}

func (c *component) LastHealthStates() apiv1.HealthStates {
	c.lastMu.RLock()
	lastData := c.lastData
	c.lastMu.RUnlock()
	return lastData.getLastHealthStates()
}

func (c *component) Events(ctx context.Context, since time.Time) (apiv1.Events, error) {
	if c.eventBucket == nil {
		return nil, nil
	}
	return c.eventBucket.Get(ctx, since)
}

func (c *component) Close() error {
	log.Logger.Debugw("closing component")

	c.cancel()

	return nil
}

func (c *component) Check() components.CheckResult {
	log.Logger.Infow("checking nvidia gpu clock events for hw slowdown")

	d := &Data{
		ts: time.Now().UTC(),
	}
	defer func() {
		c.lastMu.Lock()
		c.lastData = d
		c.lastMu.Unlock()
	}()

	if c.nvmlInstance == nil {
		d.health = apiv1.HealthStateTypeHealthy
		d.reason = "NVIDIA NVML instance is nil"
		return d
	}
	if !c.nvmlInstance.NVMLExists() {
		d.health = apiv1.HealthStateTypeHealthy
		d.reason = "NVIDIA NVML is not loaded"
		return d
	}

	if c.getSystemDriverVersionFunc != nil {
		driverVersion, err := c.getSystemDriverVersionFunc()
		if err != nil {
			d.health = apiv1.HealthStateTypeUnhealthy
			d.reason = fmt.Sprintf("error getting driver version: %s", err)
			return d
		}
		major, _, _, err := c.parseDriverVersionFunc(driverVersion)
		if err != nil {
			d.health = apiv1.HealthStateTypeUnhealthy
			d.reason = fmt.Sprintf("error parsing driver version: %s", err)
			return d
		}
		if !c.checkClockEventsSupportedFunc(major) {
			d.health = apiv1.HealthStateTypeHealthy
			d.reason = fmt.Sprintf("clock events not supported for driver version %s", driverVersion)
			return d
		}
	}

	devs := c.nvmlInstance.Devices()
	for uuid, dev := range devs {
		supported, err := c.getClockEventsSupportedFunc(dev)
		if err != nil {
			d.health = apiv1.HealthStateTypeUnhealthy
			d.err = err
			d.reason = fmt.Sprintf("error getting clock events supported for device %s", uuid)
			return d
		}

		if !supported {
			d.health = apiv1.HealthStateTypeHealthy
			d.reason = fmt.Sprintf("clock events not supported for device %s", uuid)
			return d
		}

		clockEvents, err := c.getClockEventsFunc(uuid, dev)
		if err != nil {
			d.health = apiv1.HealthStateTypeUnhealthy
			d.err = err
			d.reason = fmt.Sprintf("error getting clock events for gpu %s", uuid)
			return d
		}

		if clockEvents.HWSlowdown {
			metricHWSlowdown.With(prometheus.Labels{pkgmetrics.MetricLabelKey: uuid}).Set(float64(1))
		} else {
			metricHWSlowdown.With(prometheus.Labels{pkgmetrics.MetricLabelKey: uuid}).Set(float64(0))
		}

		if clockEvents.HWSlowdownThermal {
			metricHWSlowdownThermal.With(prometheus.Labels{pkgmetrics.MetricLabelKey: uuid}).Set(float64(1))
		} else {
			metricHWSlowdownThermal.With(prometheus.Labels{pkgmetrics.MetricLabelKey: uuid}).Set(float64(0))
		}

		if clockEvents.HWSlowdownPowerBrake {
			metricHWSlowdownPowerBrake.With(prometheus.Labels{pkgmetrics.MetricLabelKey: uuid}).Set(float64(1))
		} else {
			metricHWSlowdownPowerBrake.With(prometheus.Labels{pkgmetrics.MetricLabelKey: uuid}).Set(float64(0))
		}

		d.ClockEvents = append(d.ClockEvents, clockEvents)

		ev := clockEvents.Event()
		if ev == nil {
			// no clock event found, skip
			continue
		}

		if c.eventBucket != nil {
			log.Logger.Infow("inserting clock events to db", "gpu_uuid", uuid)

			cctx, ccancel := context.WithTimeout(c.ctx, 15*time.Second)
			found, err := c.eventBucket.Find(cctx, *ev)
			ccancel()
			if err != nil {
				log.Logger.Errorw("failed to find clock events from db", "error", err, "gpu_uuid", uuid)

				d.health = apiv1.HealthStateTypeUnhealthy
				d.err = err
				d.reason = fmt.Sprintf("error finding clock events for gpu %s", uuid)
				return d
			}
			if found != nil {
				log.Logger.Infow("clock event already found in db", "gpu_uuid", uuid)
				continue
			}

			if err := c.eventBucket.Insert(c.ctx, *ev); err != nil {
				log.Logger.Errorw("failed to insert event", "error", err)

				d.health = apiv1.HealthStateTypeUnhealthy
				d.err = err
				d.reason = fmt.Sprintf("error inserting clock events for gpu %s", uuid)
				return d
			}
			log.Logger.Infow("inserted clock events to db", "gpu_uuid", uuid)
		}
	}

	if c.evaluationWindow == 0 {
		// no time window to evaluate /state
		d.health = apiv1.HealthStateTypeHealthy
		d.reason = "no time window to evaluate states"
		return d
	}

	if c.eventBucket == nil {
		d.health = apiv1.HealthStateTypeHealthy
		d.reason = "no event bucket"
		return d
	}

	since := time.Now().UTC().Add(-c.evaluationWindow)
	cctx, ccancel := context.WithTimeout(c.ctx, 15*time.Second)
	latestEvents, err := c.eventBucket.Get(cctx, since)
	ccancel()
	if err != nil {
		log.Logger.Errorw("failed to get clock events from db", "error", err)

		d.err = err
		d.health = apiv1.HealthStateTypeUnhealthy
		d.reason = fmt.Sprintf("error getting clock events from db: %s", err)
		return d
	}

	if len(latestEvents) == 0 {
		d.health = apiv1.HealthStateTypeHealthy
		d.reason = "no clock events found"
		return d
	}

	eventsByMinute := make(map[int]struct{})
	for _, event := range latestEvents {
		minute := int(event.Time.Unix() / 60) // unix seconds to minutes
		eventsByMinute[minute] = struct{}{}
	}
	totalEvents := len(eventsByMinute)
	minutes := c.evaluationWindow.Minutes()
	freqPerMin := float64(totalEvents) / minutes

	if freqPerMin < c.threshold {
		// hw slowdown events happened but within its threshold
		d.health = apiv1.HealthStateTypeHealthy
		d.reason = fmt.Sprintf("hw slowdown events frequency per minute %.2f (total events per minute count %d) is less than threshold %.2f for the last %s", freqPerMin, totalEvents, c.threshold, c.evaluationWindow)
		return d
	}

	// hw slowdown events happened and beyond its threshold
	d.health = apiv1.HealthStateTypeUnhealthy
	d.reason = fmt.Sprintf("hw slowdown events frequency per minute %.2f (total events per minute count %d) exceeded threshold %.2f for the last %s", freqPerMin, totalEvents, c.threshold, c.evaluationWindow)
	d.suggestedActions = &apiv1.SuggestedActions{
		RepairActions: []apiv1.RepairActionType{
			apiv1.RepairActionTypeHardwareInspection,
		},
		DeprecatedDescriptions: []string{
			"Hardware slowdown are often caused by GPU overheating or power supply unit (PSU) failing, please do a hardware inspection to mitigate the issue",
		},
	}

	return d
}

var _ components.CheckResult = &Data{}

type Data struct {
	ClockEvents []nvidianvml.ClockEvents `json:"clock_events,omitempty"`

	// timestamp of the last check
	ts time.Time
	// error from the last check
	err error

	// tracks the healthy evaluation result of the last check
	health apiv1.HealthStateType
	// tracks the reason of the last check
	reason string
	// tracks the suggested actions of the last check
	suggestedActions *apiv1.SuggestedActions
}

func (d *Data) String() string {
	if d == nil {
		return ""
	}
	if len(d.ClockEvents) == 0 {
		return "no data"
	}

	buf := bytes.NewBuffer(nil)
	table := tablewriter.NewWriter(buf)
	table.SetAlignment(tablewriter.ALIGN_CENTER)
	table.SetHeader([]string{"GPU UUID", "HW Slowdown", "HW Slowdown Thermal", "HW Slowdown Power Brake", "Reasons"})
	for _, event := range d.ClockEvents {
		table.Append([]string{event.UUID, fmt.Sprintf("%t", event.HWSlowdown), fmt.Sprintf("%t", event.HWSlowdownThermal), fmt.Sprintf("%t", event.HWSlowdownPowerBrake), strings.Join(event.Reasons, ", ")})
	}
	table.Render()

	return buf.String()
}

func (d *Data) Summary() string {
	if d == nil {
		return ""
	}
	return d.reason
}

func (d *Data) HealthState() apiv1.HealthStateType {
	if d == nil {
		return ""
	}
	return d.health
}

func (d *Data) getError() string {
	if d == nil || d.err == nil {
		return ""
	}
	return d.err.Error()
}

func (d *Data) getLastHealthStates() apiv1.HealthStates {
	if d == nil {
		return apiv1.HealthStates{
			{
				Name:   Name,
				Health: apiv1.HealthStateTypeHealthy,
				Reason: "no data yet",
			},
		}
	}

	state := apiv1.HealthState{
		Name:             Name,
		Reason:           d.reason,
		Error:            d.getError(),
		Health:           d.health,
		SuggestedActions: d.suggestedActions,
	}

	b, _ := json.Marshal(d)
	state.DeprecatedExtraInfo = map[string]string{
		"data":     string(b),
		"encoding": "json",
	}
	return apiv1.HealthStates{state}
}
