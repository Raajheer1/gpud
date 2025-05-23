package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leptonai/gpud/pkg/sqlite"

	apiv1 "github.com/leptonai/gpud/api/v1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func Test_defaultTableName(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple name",
			input:    "test",
			expected: fmt.Sprintf("components_test_events_%s", schemaVersion),
		},
		{
			name:     "name with spaces",
			input:    "test component",
			expected: fmt.Sprintf("components_test_component_events_%s", schemaVersion),
		},
		{
			name:     "name with hyphens",
			input:    "test-component",
			expected: fmt.Sprintf("components_test_component_events_%s", schemaVersion),
		},
		{
			name:     "mixed case",
			input:    "TestComponent",
			expected: fmt.Sprintf("components_testcomponent_events_%s", schemaVersion),
		},
		{
			name:     "complex name",
			input:    "Test Component-Name",
			expected: fmt.Sprintf("components_test_component_name_events_%s", schemaVersion),
		},
		{
			name:     "empty string",
			input:    "",
			expected: fmt.Sprintf("components__events_%s", schemaVersion),
		},
		{
			name:     "multiple spaces and hyphens",
			input:    "test  component--name",
			expected: fmt.Sprintf("components_test_component_name_events_%s", schemaVersion),
		},
	}

	for _, tc := range testCases {
		tc := tc // capture range variable
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := defaultTableName(tc.input)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestTableInsertsReads(t *testing.T) {
	t.Parallel()

	testTableName := "test_table"

	dbRW, dbRO, cleanup := sqlite.OpenTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	store, err := New(dbRW, dbRO, 0)
	assert.NoError(t, err)
	bucket, err := store.Bucket(testTableName)
	assert.NoError(t, err)
	defer bucket.Close()

	first := time.Now().UTC()

	events := apiv1.Events{}
	eventsN := 10
	for i := 0; i < eventsN; i++ {
		events = append(events, apiv1.Event{
			Time:    metav1.Time{Time: first.Add(time.Duration(i) * time.Second)},
			Name:    "kmsg",
			Type:    apiv1.EventTypeWarning,
			Message: fmt.Sprintf("OOM event %d occurred", i),
			DeprecatedSuggestedActions: &apiv1.SuggestedActions{
				DeprecatedDescriptions: []string{fmt.Sprintf("oom_reaper: reaped process %d (vector), now anon-rss:0kB, file-rss:0kB, shmem-rss:0", i)},
			},
		})
	}

	for _, ev := range events {
		assert.NoError(t, bucket.Insert(ctx, ev))
	}

	events, err = bucket.Get(ctx, first.Add(-30*time.Second))
	assert.NoError(t, err)
	assert.Equal(t, eventsN, len(events))

	// make sure timestamp is in descending order
	for i := 1; i < len(events); i++ {
		assert.Greater(t, events[i-1].Time.Unix(), events[i].Time.Unix(), "timestamps should be in descending order")
		// Since events are returned in descending order (newest first),
		// the message index should be eventsN - (i + 1) for the current event
		expectedMsg := fmt.Sprintf("OOM event %d occurred", eventsN-(i+1))
		assert.Equal(t, expectedMsg, events[i].Message, "messages should match in descending order")
	}
}

func TestGetEventsTimeRange(t *testing.T) {
	t.Parallel()

	testTableName := "test_table"

	dbRW, dbRO, cleanup := sqlite.OpenTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	store, err := New(dbRW, dbRO, 0)
	assert.NoError(t, err)
	bucket, err := store.Bucket(testTableName)
	assert.NoError(t, err)
	defer bucket.Close()

	baseTime := time.Now().UTC()
	events := apiv1.Events{
		{
			Time: metav1.Time{Time: baseTime.Add(-10 * time.Minute)},
			Name: "kmsg",
			Type: apiv1.EventTypeWarning,
			DeprecatedSuggestedActions: &apiv1.SuggestedActions{
				DeprecatedDescriptions: []string{"old event"},
			},
		},
		{
			Time: metav1.Time{Time: baseTime.Add(-5 * time.Minute)},
			Name: "kmsg",
			Type: apiv1.EventTypeWarning,
			DeprecatedSuggestedActions: &apiv1.SuggestedActions{
				DeprecatedDescriptions: []string{"mid event"},
			},
		},
		{
			Time: metav1.Time{Time: baseTime},
			Name: "kmsg",
			Type: apiv1.EventTypeWarning,
			DeprecatedSuggestedActions: &apiv1.SuggestedActions{
				DeprecatedDescriptions: []string{"recent event"},
			},
		},
	}

	for _, ev := range events {
		assert.NoError(t, bucket.Insert(ctx, ev))
	}

	// Test getting all events
	allEvents, err := bucket.Get(ctx, baseTime.Add(-15*time.Minute))
	assert.NoError(t, err)
	assert.Equal(t, 3, len(allEvents))

	// Test getting recent events only
	recentEvents, err := bucket.Get(ctx, baseTime.Add(-2*time.Minute))
	assert.NoError(t, err)
	assert.Equal(t, 1, len(recentEvents))
	assert.Equal(t, "recent event", recentEvents[0].DeprecatedSuggestedActions.DeprecatedDescriptions[0])
}

func TestEmptyResults(t *testing.T) {
	t.Parallel()

	testTableName := "test_table"

	dbRW, dbRO, cleanup := sqlite.OpenTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	store, err := New(dbRW, dbRO, 0)
	assert.NoError(t, err)
	bucket, err := store.Bucket(testTableName)
	assert.NoError(t, err)
	defer bucket.Close()

	// Test getting events from empty table
	events, err := bucket.Get(ctx, time.Now().Add(-1*time.Hour))
	assert.NoError(t, err)
	assert.Nil(t, events)
}

func TestMultipleEventTypes(t *testing.T) {
	t.Parallel()

	testTableName := "test_table"

	dbRW, dbRO, cleanup := sqlite.OpenTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	store, err := New(dbRW, dbRO, 0)
	assert.NoError(t, err)
	bucket, err := store.Bucket(testTableName)
	assert.NoError(t, err)
	defer bucket.Close()

	baseTime := time.Now().UTC()
	events := apiv1.Events{
		{
			Time: metav1.Time{Time: baseTime},
			Name: "kmsg",
			Type: apiv1.EventTypeWarning,
			DeprecatedSuggestedActions: &apiv1.SuggestedActions{
				DeprecatedDescriptions: []string{"oom event"},
			},
		},
		{
			Time: metav1.Time{Time: baseTime.Add(1 * time.Second)},
			Name: "syslog",
			Type: apiv1.EventTypeWarning,
			DeprecatedSuggestedActions: &apiv1.SuggestedActions{
				DeprecatedDescriptions: []string{"edac event"},
			},
		},
		{
			Time: metav1.Time{Time: baseTime.Add(2 * time.Second)},
			Name: "kmsg",
			Type: apiv1.EventTypeWarning,
			DeprecatedSuggestedActions: &apiv1.SuggestedActions{
				DeprecatedDescriptions: []string{"cgroup event"},
			},
		},
	}

	for _, ev := range events {
		assert.NoError(t, bucket.Insert(ctx, ev))
	}

	// Get all events
	results, err := bucket.Get(ctx, baseTime.Add(-1*time.Second))
	assert.NoError(t, err)
	assert.Equal(t, 3, len(results))

	// Verify events are in descending order
	assert.Equal(t, apiv1.EventTypeWarning, results[0].Type)
	assert.Equal(t, apiv1.EventTypeWarning, results[1].Type)
	assert.Equal(t, apiv1.EventTypeWarning, results[2].Type)
}

func TestPurgePartial(t *testing.T) {
	t.Parallel()

	testTableName := "test_table"

	dbRW, dbRO, cleanup := sqlite.OpenTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	store, err := New(dbRW, dbRO, 0)
	assert.NoError(t, err)
	bucket, err := store.Bucket(testTableName)
	assert.NoError(t, err)
	defer bucket.Close()

	baseTime := time.Now().UTC()
	events := apiv1.Events{
		{
			Time:                metav1.Time{Time: baseTime.Add(-10 * time.Minute)},
			Name:                "kmsg",
			Type:                apiv1.EventTypeWarning,
			DeprecatedExtraInfo: map[string]string{"id": "old_event"},
			DeprecatedSuggestedActions: &apiv1.SuggestedActions{
				DeprecatedDescriptions: []string{"old event"},
			},
		},
		{
			Time:                metav1.Time{Time: baseTime},
			Name:                "kmsg",
			Type:                apiv1.EventTypeWarning,
			DeprecatedExtraInfo: map[string]string{"id": "new_event"},
			DeprecatedSuggestedActions: &apiv1.SuggestedActions{
				DeprecatedDescriptions: []string{"recent event"},
			},
		},
	}

	for _, ev := range events {
		assert.NoError(t, bucket.Insert(ctx, ev))
	}

	// Purge only old events
	deleted, err := bucket.Purge(ctx, baseTime.Add(-5*time.Minute).Unix())
	assert.NoError(t, err)
	assert.Equal(t, 1, deleted)

	// Verify only recent event remains
	remaining, err := bucket.Get(ctx, baseTime.Add(-15*time.Minute))
	assert.NoError(t, err)
	assert.Equal(t, 1, len(remaining))
	extraInfoJSON, err := json.Marshal(remaining[0].DeprecatedExtraInfo)
	assert.NoError(t, err)
	assert.Equal(t, `{"id":"new_event"}`, string(extraInfoJSON))

	// Try to find old event by ExtraInfo
	oldEvent := apiv1.Event{
		Time:                metav1.Time{Time: baseTime.Add(-10 * time.Minute)},
		Name:                "test",
		Type:                apiv1.EventTypeWarning,
		DeprecatedExtraInfo: map[string]string{"id": "old_event"},
	}
	found, err := bucket.Find(ctx, oldEvent)
	assert.NoError(t, err)
	assert.Nil(t, found, "Old event should not be found after purge")
}

func TestFindEvent(t *testing.T) {
	t.Parallel()

	testTableName := "test_table"

	dbRW, dbRO, cleanup := sqlite.OpenTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	store, err := New(dbRW, dbRO, 0)
	assert.NoError(t, err)
	bucket, err := store.Bucket(testTableName)
	assert.NoError(t, err)
	defer bucket.Close()

	baseTime := time.Now().UTC()
	testEvent := apiv1.Event{
		Time:                metav1.Time{Time: baseTime.Add(-10 * time.Minute)},
		Name:                "kmsg",
		Type:                apiv1.EventTypeWarning,
		DeprecatedExtraInfo: map[string]string{"a": "b"},
		DeprecatedSuggestedActions: &apiv1.SuggestedActions{
			DeprecatedDescriptions: []string{"old event"},
		},
	}

	// Test finding non-existent event
	found, err := bucket.Find(ctx, testEvent)
	assert.NoError(t, err)
	assert.Nil(t, found)

	// Insert and find the event
	assert.NoError(t, bucket.Insert(ctx, testEvent))

	found, err = bucket.Find(ctx, testEvent)
	assert.NoError(t, err)
	assert.NotNil(t, found)
	assert.Equal(t, testEvent.Time.Unix(), found.Time.Unix())
	assert.Equal(t, testEvent.Name, found.Name)
	assert.Equal(t, testEvent.Type, found.Type)
	assert.Equal(t, testEvent.DeprecatedExtraInfo, found.DeprecatedExtraInfo)
	assert.Equal(t, testEvent.DeprecatedSuggestedActions.DeprecatedDescriptions[0], found.DeprecatedSuggestedActions.DeprecatedDescriptions[0])
}

func TestFindEventPartialMatch(t *testing.T) {
	t.Parallel()

	testTableName := "test_table"

	dbRW, dbRO, cleanup := sqlite.OpenTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	store, err := New(dbRW, dbRO, 0)
	assert.NoError(t, err)
	bucket, err := store.Bucket(testTableName)
	assert.NoError(t, err)
	defer bucket.Close()

	baseTime := time.Now().UTC()
	testEvent := apiv1.Event{
		Time:                metav1.Time{Time: baseTime},
		Name:                "kmsg",
		Type:                apiv1.EventTypeWarning,
		DeprecatedExtraInfo: map[string]string{"a": "b"},
		DeprecatedSuggestedActions: &apiv1.SuggestedActions{
			DeprecatedDescriptions: []string{"original details"},
		},
	}

	assert.NoError(t, bucket.Insert(ctx, testEvent))

	// Test finding with matching timestamp/source/type but different details
	searchEvent := apiv1.Event{
		Time:                metav1.Time{Time: testEvent.Time.Time},
		Name:                testEvent.Name,
		Type:                testEvent.Type,
		DeprecatedExtraInfo: testEvent.DeprecatedExtraInfo,
		DeprecatedSuggestedActions: &apiv1.SuggestedActions{
			DeprecatedDescriptions: []string{"different details"},
		},
	}

	found, err := bucket.Find(ctx, searchEvent)
	assert.NoError(t, err)
	assert.Nil(t, found)
}

func TestFindEventMultipleMatches(t *testing.T) {
	t.Parallel()

	testTableName := "test_table"

	dbRW, dbRO, cleanup := sqlite.OpenTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	store, err := New(dbRW, dbRO, 0)
	assert.NoError(t, err)
	bucket, err := store.Bucket(testTableName)
	assert.NoError(t, err)
	defer bucket.Close()

	baseTime := time.Now().UTC()
	events := apiv1.Events{
		{
			Time:                metav1.Time{Time: baseTime},
			Name:                "kmsg",
			Type:                apiv1.EventTypeWarning,
			DeprecatedExtraInfo: map[string]string{"a": "b", "c": "d"},
			DeprecatedSuggestedActions: &apiv1.SuggestedActions{
				DeprecatedDescriptions: []string{"first event"},
			},
		},
		{
			Time:                metav1.Time{Time: baseTime},
			Name:                "kmsg",
			Type:                apiv1.EventTypeWarning,
			DeprecatedExtraInfo: map[string]string{"a": "b"},
			DeprecatedSuggestedActions: &apiv1.SuggestedActions{
				DeprecatedDescriptions: []string{"second event"},
			},
		},
	}

	// Insert multiple events with same timestamp/source/type
	for _, ev := range events {
		assert.NoError(t, bucket.Insert(ctx, ev))
	}

	// Search should return the first matching event
	searchEvent := apiv1.Event{
		Time:                metav1.Time{Time: baseTime},
		Name:                "kmsg",
		Type:                apiv1.EventTypeWarning,
		DeprecatedExtraInfo: map[string]string{"a": "b"},
	}

	found, err := bucket.Find(ctx, searchEvent)
	assert.NoError(t, err)
	assert.NotNil(t, found)

	// Should match one of the events
	foundMatch := false
	for _, ev := range events {
		if found.DeprecatedSuggestedActions.DeprecatedDescriptions[0] == ev.DeprecatedSuggestedActions.DeprecatedDescriptions[0] {
			foundMatch = true
			break
		}
	}
	assert.True(t, foundMatch, "Found event should match one of the inserted events")
}

func TestEventWithIDs(t *testing.T) {
	t.Parallel()

	testTableName := "test_table"

	dbRW, dbRO, cleanup := sqlite.OpenTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	store, err := New(dbRW, dbRO, 0)
	assert.NoError(t, err)
	bucket, err := store.Bucket(testTableName)
	assert.NoError(t, err)
	defer bucket.Close()

	baseTime := time.Now().UTC()
	event := apiv1.Event{
		Time: metav1.Time{Time: baseTime},
		Name: "nvidia-smi",
		Type: apiv1.EventTypeWarning,
		DeprecatedExtraInfo: map[string]string{
			"xid":      "123",
			"gpu_uuid": "gpu-123",
		},
		DeprecatedSuggestedActions: &apiv1.SuggestedActions{
			DeprecatedDescriptions: []string{"GPU error details"},
		},
	}

	// Test insert and find with ExtraInfo
	err = bucket.Insert(ctx, event)
	assert.NoError(t, err)

	found, err := bucket.Find(ctx, event)
	assert.NoError(t, err)
	assert.NotNil(t, found)
	assert.Equal(t, event.DeprecatedExtraInfo, found.DeprecatedExtraInfo)

	// Test find with partial ExtraInfo match
	partialEvent := apiv1.Event{
		Time: metav1.Time{Time: event.Time.Time},
		Name: event.Name,
		Type: event.Type,
	}

	found, err = bucket.Find(ctx, partialEvent)
	assert.NoError(t, err)
	assert.Nil(t, found, "Should not find event with different ExtraInfo")

	// Test find with different ExtraInfo
	differentEvent := apiv1.Event{
		Time: metav1.Time{Time: event.Time.Time},
		Name: event.Name,
		Type: event.Type,
		DeprecatedExtraInfo: map[string]string{
			"xid":      "different",
			"gpu_uuid": "different-gpu",
		},
	}

	found, err = bucket.Find(ctx, differentEvent)
	assert.NoError(t, err)
	assert.Nil(t, found, "Should not find event with different ExtraInfo")
}

func TestNullEventIDs(t *testing.T) {
	t.Parallel()

	testTableName := "test_table"

	dbRW, dbRO, cleanup := sqlite.OpenTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	store, err := New(dbRW, dbRO, 0)
	assert.NoError(t, err)
	bucket, err := store.Bucket(testTableName)
	assert.NoError(t, err)
	defer bucket.Close()

	baseTime := time.Now().UTC()
	event := apiv1.Event{
		Time:                metav1.Time{Time: baseTime},
		Name:                "kmsg",
		Type:                apiv1.EventTypeWarning,
		DeprecatedExtraInfo: map[string]string{},
		DeprecatedSuggestedActions: &apiv1.SuggestedActions{
			DeprecatedDescriptions: []string{"Event with null ExtraInfo"},
		},
	}

	// Test insert and find with null ExtraInfo
	err = bucket.Insert(ctx, event)
	assert.NoError(t, err)

	found, err := bucket.Find(ctx, event)
	assert.NoError(t, err)
	assert.NotNil(t, found)
	assert.Equal(t, len(found.DeprecatedExtraInfo), 0)
}

func TestPurgeWithEventIDs(t *testing.T) {
	t.Parallel()

	testTableName := "test_table"

	dbRW, dbRO, cleanup := sqlite.OpenTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	store, err := New(dbRW, dbRO, 0)
	assert.NoError(t, err)
	bucket, err := store.Bucket(testTableName)
	assert.NoError(t, err)
	defer bucket.Close()

	baseTime := time.Now().UTC()
	events := apiv1.Events{
		{
			Time:                metav1.Time{Time: baseTime.Add(-10 * time.Minute)},
			Name:                "test",
			Type:                apiv1.EventTypeWarning,
			DeprecatedExtraInfo: map[string]string{"id": "old_event"},
			DeprecatedSuggestedActions: &apiv1.SuggestedActions{
				DeprecatedDescriptions: []string{"old event"},
			},
		},
		{
			Time:                metav1.Time{Time: baseTime},
			Name:                "test",
			Type:                apiv1.EventTypeWarning,
			DeprecatedExtraInfo: map[string]string{"id": "new_event"},
			DeprecatedSuggestedActions: &apiv1.SuggestedActions{
				DeprecatedDescriptions: []string{"new event"},
			},
		},
	}

	for _, event := range events {
		err = bucket.Insert(ctx, event)
		assert.NoError(t, err)
	}

	// purge old events
	deleted, err := bucket.Purge(ctx, baseTime.Add(-5*time.Minute).Unix())
	assert.NoError(t, err)
	assert.Equal(t, 1, deleted)

	// Verify only new event remains with correct ExtraInfo
	remaining, err := bucket.Get(ctx, baseTime.Add(-15*time.Minute))
	assert.NoError(t, err)
	assert.Equal(t, 1, len(remaining))
	extraInfoJSON, err := json.Marshal(remaining[0].DeprecatedExtraInfo)
	assert.NoError(t, err)
	assert.Equal(t, `{"id":"new_event"}`, string(extraInfoJSON))

	// Try to find old event by ExtraInfo
	oldEvent := apiv1.Event{
		Time:                metav1.Time{Time: baseTime.Add(-10 * time.Minute)},
		Name:                "test",
		Type:                apiv1.EventTypeWarning,
		DeprecatedExtraInfo: map[string]string{"id": "old_event"},
	}
	found, err := bucket.Find(ctx, oldEvent)
	assert.NoError(t, err)
	assert.Nil(t, found, "Old event should not be found after purge")
}

func TestInvalidTableName(t *testing.T) {
	t.Parallel()

	dbRW, dbRO, cleanup := sqlite.OpenTestDB(t)
	defer cleanup()

	// Test with invalid table name
	store, err := New(dbRW, dbRO, 0)
	assert.NoError(t, err)
	_, err = store.Bucket("invalid;table;name")
	assert.Error(t, err)
}

func TestContextCancellation(t *testing.T) {
	t.Parallel()

	testTableName := "test_table"

	dbRW, dbRO, cleanup := sqlite.OpenTestDB(t)
	defer cleanup()

	store, err := New(dbRW, dbRO, 0)
	assert.NoError(t, err)
	bucket, err := store.Bucket(testTableName)
	assert.NoError(t, err)
	defer bucket.Close()

	// Test with canceled context
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	event := apiv1.Event{
		Time: metav1.Time{Time: time.Now().UTC()},
		Name: "test",
		Type: apiv1.EventTypeWarning,
		DeprecatedSuggestedActions: &apiv1.SuggestedActions{
			DeprecatedDescriptions: []string{"Test details"},
		},
	}

	err = bucket.Insert(canceledCtx, event)
	assert.Error(t, err)

	_, err = bucket.Find(canceledCtx, event)
	assert.Error(t, err)

	_, err = bucket.Get(canceledCtx, time.Now().Add(-1*time.Hour))
	assert.Error(t, err)
}

func TestConcurrentAccess(t *testing.T) {
	t.Parallel()

	testTableName := "test_table"

	dbRW, dbRO, cleanup := sqlite.OpenTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	store, err := New(dbRW, dbRO, 0)
	assert.NoError(t, err)
	bucket, err := store.Bucket(testTableName)
	assert.NoError(t, err)
	defer bucket.Close()

	baseTime := time.Now().UTC()
	eventCount := 100
	done := make(chan bool)

	// Concurrent inserts
	go func() {
		for i := 0; i < eventCount; i++ {
			event := apiv1.Event{
				Time: metav1.Time{Time: baseTime.Add(time.Duration(i) * time.Second)},
				Name: "concurrent",
				Type: apiv1.EventTypeWarning,
				DeprecatedSuggestedActions: &apiv1.SuggestedActions{
					DeprecatedDescriptions: []string{fmt.Sprintf("Concurrent event %d", i)},
				},
			}
			assert.NoError(t, bucket.Insert(ctx, event))
		}
		done <- true
	}()

	// Concurrent reads
	go func() {
		for i := 0; i < eventCount; i++ {
			_, err := bucket.Get(ctx, baseTime.Add(-1*time.Hour))
			assert.NoError(t, err)
		}
		done <- true
	}()

	// Wait for both goroutines to complete
	<-done
	<-done

	// Verify final count
	events, err := bucket.Get(ctx, baseTime.Add(-1*time.Hour))
	assert.NoError(t, err)
	assert.Equal(t, eventCount, len(events))
}

func TestSpecialCharactersInEvents(t *testing.T) {
	t.Parallel()

	testTableName := "test_table"

	dbRW, dbRO, cleanup := sqlite.OpenTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	store, err := New(dbRW, dbRO, 0)
	assert.NoError(t, err)
	bucket, err := store.Bucket(testTableName)
	assert.NoError(t, err)
	defer bucket.Close()

	events := apiv1.Events{
		{
			Time:                metav1.Time{Time: time.Now().UTC()},
			Name:                "test;source",
			Type:                apiv1.EventTypeWarning,
			Message:             "message with special chars: !@#$%^&*()",
			DeprecatedExtraInfo: map[string]string{"special chars": "!@#$%^&*()"},
			DeprecatedSuggestedActions: &apiv1.SuggestedActions{
				DeprecatedDescriptions: []string{"details with special chars"},
			},
		},
		{
			Time:                metav1.Time{Time: time.Now().UTC()},
			Name:                "unicode_source_🔥",
			Type:                apiv1.EventTypeWarning,
			Message:             "unicode message: 你好",
			DeprecatedExtraInfo: map[string]string{"unicode info": "你好"},
			DeprecatedSuggestedActions: &apiv1.SuggestedActions{
				DeprecatedDescriptions: []string{"unicode details: 世界！"},
			},
		},
	}

	// Test insert and retrieval of events with special characters
	for _, event := range events {
		err = bucket.Insert(ctx, event)
		assert.NoError(t, err)

		found, err := bucket.Find(ctx, event)
		assert.NoError(t, err)
		assert.NotNil(t, found)
		assert.Equal(t, event.Name, found.Name)
		assert.Equal(t, event.Type, found.Type)
		assert.Equal(t, event.Message, found.Message)
		assert.Equal(t, event.DeprecatedExtraInfo, found.DeprecatedExtraInfo)
		assert.Equal(t, event.DeprecatedSuggestedActions.DeprecatedDescriptions[0], found.DeprecatedSuggestedActions.DeprecatedDescriptions[0])
	}
}

func TestLargeEventDetails(t *testing.T) {
	t.Parallel()

	testTableName := "test_table"

	dbRW, dbRO, cleanup := sqlite.OpenTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	store, err := New(dbRW, dbRO, 0)
	assert.NoError(t, err)
	bucket, err := store.Bucket(testTableName)
	assert.NoError(t, err)
	defer bucket.Close()

	// Create a large event detail string (100KB)
	largeDetail := make([]byte, 100*1024)
	for i := range largeDetail {
		largeDetail[i] = byte('a' + (i % 26))
	}

	event := apiv1.Event{
		Time: metav1.Time{Time: time.Now().UTC()},
		Name: "test",
		Type: apiv1.EventTypeWarning,
		DeprecatedSuggestedActions: &apiv1.SuggestedActions{
			DeprecatedDescriptions: []string{string(largeDetail)},
		},
	}

	err = bucket.Insert(ctx, event)
	assert.NoError(t, err)

	found, err := bucket.Find(ctx, event)
	assert.NoError(t, err)
	assert.NotNil(t, found)
	assert.Equal(t, event.DeprecatedSuggestedActions.DeprecatedDescriptions[0], found.DeprecatedSuggestedActions.DeprecatedDescriptions[0])
}

func TestTimestampBoundaries(t *testing.T) {
	t.Parallel()

	testTableName := "test_table"

	dbRW, dbRO, cleanup := sqlite.OpenTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	store, err := New(dbRW, dbRO, 0)
	assert.NoError(t, err)
	bucket, err := store.Bucket(testTableName)
	assert.NoError(t, err)
	defer bucket.Close()

	timestamps := []int64{
		0,                  // Unix epoch
		-1,                 // Before Unix epoch
		1 << 32,            // Large timestamp
		-(1 << 31),         // Large negative timestamp
		time.Now().Unix(),  // Current time
		1 << 62,            // Very large timestamp
		-((1 << 62) + 100), // Very large negative timestamp
	}

	for _, ts := range timestamps {
		event := apiv1.Event{
			Time: metav1.Time{Time: time.Unix(ts, 0)},
			Name: "test",
			Type: apiv1.EventTypeWarning,
			DeprecatedSuggestedActions: &apiv1.SuggestedActions{
				DeprecatedDescriptions: []string{fmt.Sprintf("timestamp: %d", ts)},
			},
		}

		err = bucket.Insert(ctx, event)
		assert.NoError(t, err)

		found, err := bucket.Find(ctx, event)
		assert.NoError(t, err)
		assert.NotNil(t, found)
		assert.Equal(t, ts, found.Time.Unix())
	}

	// Test retrieval with various time ranges
	events, err := bucket.Get(ctx, time.Unix(-(1<<63), 0)) // Get all events
	assert.NoError(t, err)
	assert.Equal(t, len(timestamps), len(events))

	events, err = bucket.Get(ctx, time.Unix(1<<63-1, 0)) // Future time
	assert.NoError(t, err)
	assert.Nil(t, events)
}

func TestConcurrentWritesWithDifferentIDs(t *testing.T) {
	t.Parallel()

	testTableName := "test_table"

	dbRW, dbRO, cleanup := sqlite.OpenTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	store, err := New(dbRW, dbRO, 0)
	assert.NoError(t, err)
	bucket, err := store.Bucket(testTableName)
	assert.NoError(t, err)
	defer bucket.Close()

	baseTime := time.Now().UTC()
	eventCount := 100
	done := make(chan bool)

	// Concurrent inserts
	go func() {
		for i := 0; i < eventCount; i++ {
			event := apiv1.Event{
				Time:                metav1.Time{Time: baseTime.Add(time.Duration(i) * time.Second)},
				Name:                "concurrent",
				Type:                apiv1.EventTypeWarning,
				DeprecatedExtraInfo: map[string]string{fmt.Sprintf("info_%d", i): fmt.Sprintf("Concurrent event %d", i)},
				DeprecatedSuggestedActions: &apiv1.SuggestedActions{
					DeprecatedDescriptions: []string{fmt.Sprintf("Concurrent event %d", i)},
				},
			}
			assert.NoError(t, bucket.Insert(ctx, event))
		}
		done <- true
	}()

	// Concurrent reads
	go func() {
		for i := 0; i < eventCount; i++ {
			event := apiv1.Event{
				Time:                metav1.Time{Time: baseTime.Add(time.Duration(i) * time.Second)},
				Name:                "concurrent",
				Type:                apiv1.EventTypeWarning,
				DeprecatedExtraInfo: map[string]string{fmt.Sprintf("info_%d", i): fmt.Sprintf("Concurrent event %d", i)},
			}
			found, err := bucket.Find(ctx, event)
			if err == nil && found != nil {
				assert.Equal(t, event.DeprecatedExtraInfo, found.DeprecatedExtraInfo)
			}
		}
		done <- true
	}()

	// Wait for both goroutines to complete
	<-done
	<-done

	// Verify all events were inserted
	events, err := bucket.Get(ctx, baseTime.Add(-1*time.Hour))
	assert.NoError(t, err)
	assert.Equal(t, eventCount, len(events))

	// Verify each event has unique info
	infoMap := make(map[string]bool)
	for _, event := range events {
		// Convert the entire ExtraInfo map to a string for comparison
		infoStr := fmt.Sprintf("%v", event.DeprecatedExtraInfo)
		assert.False(t, infoMap[infoStr], "Duplicate extra info found")
		infoMap[infoStr] = true
	}
}

func TestEventMessage(t *testing.T) {
	t.Parallel()

	testTableName := "test_table"

	dbRW, dbRO, cleanup := sqlite.OpenTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	store, err := New(dbRW, dbRO, 0)
	assert.NoError(t, err)
	bucket, err := store.Bucket(testTableName)
	assert.NoError(t, err)
	defer bucket.Close()

	baseTime := time.Now().UTC()
	events := apiv1.Events{
		{
			Time:    metav1.Time{Time: baseTime},
			Name:    "test",
			Type:    apiv1.EventTypeWarning,
			Message: "Test message with normal text",
		},
		{
			Time:    metav1.Time{Time: baseTime.Add(1 * time.Second)},
			Name:    "test",
			Type:    apiv1.EventTypeWarning,
			Message: "", // Empty message
		},
		{
			Time:    metav1.Time{Time: baseTime.Add(2 * time.Second)},
			Name:    "test",
			Type:    apiv1.EventTypeWarning,
			Message: "Message with special chars: !@#$%^&*()",
		},
		{
			Time:    metav1.Time{Time: baseTime.Add(3 * time.Second)},
			Name:    "test",
			Type:    apiv1.EventTypeWarning,
			Message: "Unicode message: 你好世界",
		},
	}

	// Test insert and retrieval
	for _, event := range events {
		err = bucket.Insert(ctx, event)
		assert.NoError(t, err)

		found, err := bucket.Find(ctx, event)
		assert.NoError(t, err)
		assert.NotNil(t, found)
		assert.Equal(t, event.Message, found.Message)
	}

	// Test finding with message as part of search criteria
	searchEvent := apiv1.Event{
		Time:    metav1.Time{Time: baseTime},
		Name:    "test",
		Type:    apiv1.EventTypeWarning,
		Message: "Test message with normal text",
	}
	found, err := bucket.Find(ctx, searchEvent)
	assert.NoError(t, err)
	assert.NotNil(t, found)
	assert.Equal(t, searchEvent.Message, found.Message)

	// Test finding with empty message
	emptyMessageEvent := apiv1.Event{
		Time:    metav1.Time{Time: baseTime.Add(1 * time.Second)},
		Name:    "test",
		Type:    apiv1.EventTypeWarning,
		Message: "",
	}
	found, err = bucket.Find(ctx, emptyMessageEvent)
	assert.NoError(t, err)
	assert.NotNil(t, found)
	assert.Equal(t, "", found.Message)

	// Test finding with non-matching message
	nonMatchingEvent := apiv1.Event{
		Time:    metav1.Time{Time: baseTime},
		Name:    "test",
		Type:    apiv1.EventTypeWarning,
		Message: "Non-matching message",
	}
	found, err = bucket.Find(ctx, nonMatchingEvent)
	assert.NoError(t, err)
	assert.Nil(t, found)

	// Test getting all events and verify messages
	allEvents, err := bucket.Get(ctx, baseTime.Add(-1*time.Second))
	assert.NoError(t, err)
	assert.Equal(t, len(events), len(allEvents))

	// Verify messages are preserved in descending timestamp order
	for i, event := range allEvents {
		expectedMsg := events[len(events)-1-i].Message
		assert.Equal(t, expectedMsg, event.Message)
	}
}

func TestNilSuggestedActions(t *testing.T) {
	t.Parallel()

	testTableName := "test_table"

	dbRW, dbRO, cleanup := sqlite.OpenTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	store, err := New(dbRW, dbRO, 0)
	assert.NoError(t, err)
	bucket, err := store.Bucket(testTableName)
	assert.NoError(t, err)
	defer bucket.Close()

	baseTime := time.Now().UTC()
	event := apiv1.Event{
		Time:                       metav1.Time{Time: baseTime},
		Name:                       "test",
		Type:                       apiv1.EventTypeWarning,
		Message:                    "Test message",
		DeprecatedExtraInfo:        map[string]string{"key": "value"},
		DeprecatedSuggestedActions: nil, // Explicitly set to nil
	}

	// Test insert and find with nil SuggestedActions
	err = bucket.Insert(ctx, event)
	assert.NoError(t, err)

	found, err := bucket.Find(ctx, event)
	assert.NoError(t, err)
	assert.NotNil(t, found)
	assert.Nil(t, found.DeprecatedSuggestedActions)
}

func TestInvalidJSONHandling(t *testing.T) {
	t.Parallel()

	testTableName := "test_table"

	dbRW, dbRO, cleanup := sqlite.OpenTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	store, err := New(dbRW, dbRO, 0)
	assert.NoError(t, err)
	bucket, err := store.Bucket(testTableName)
	assert.NoError(t, err)
	defer bucket.Close()

	// Insert a valid event first
	baseTime := time.Now().UTC()
	event := apiv1.Event{
		Time:                metav1.Time{Time: baseTime},
		Name:                "test",
		Type:                apiv1.EventTypeWarning,
		DeprecatedExtraInfo: map[string]string{"key": "value"},
		DeprecatedSuggestedActions: &apiv1.SuggestedActions{
			DeprecatedDescriptions: []string{"test action"},
		},
	}
	err = bucket.Insert(ctx, event)
	assert.NoError(t, err)

	// Manually insert invalid JSON into the database
	_, err = dbRW.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (timestamp, name, type, extra_info, suggested_actions)
		VALUES (?, ?, ?, ?, ?)`,
		bucket.Name()),
		baseTime.Add(time.Second).Unix(),
		"test",
		apiv1.EventTypeWarning,
		"{invalid_json", // Invalid JSON for ExtraInfo
		"{invalid_json", // Invalid JSON for SuggestedActions
	)
	assert.NoError(t, err)

	// Try to retrieve the events - should get error for invalid JSON
	_, err = bucket.Get(ctx, baseTime.Add(-time.Hour))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to unmarshal")
}

func TestLongEventFields(t *testing.T) {
	t.Parallel()

	testTableName := "test_table"

	dbRW, dbRO, cleanup := sqlite.OpenTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	store, err := New(dbRW, dbRO, 0)
	assert.NoError(t, err)
	bucket, err := store.Bucket(testTableName)
	assert.NoError(t, err)
	defer bucket.Close()

	// Create very long strings for various fields
	longString := strings.Repeat("a", 10000)
	longMap := make(map[string]string)
	for i := 0; i < 100; i++ {
		longMap[fmt.Sprintf("key_%d", i)] = longString
	}

	event := apiv1.Event{
		Time:                metav1.Time{Time: time.Now().UTC()},
		Name:                longString,
		Type:                apiv1.EventTypeWarning,
		Message:             longString,
		DeprecatedExtraInfo: longMap,
		DeprecatedSuggestedActions: &apiv1.SuggestedActions{
			DeprecatedDescriptions: []string{longString},
		},
	}

	// Test insert and retrieval of event with very long fields
	err = bucket.Insert(ctx, event)
	assert.NoError(t, err)

	found, err := bucket.Find(ctx, event)
	assert.NoError(t, err)
	assert.NotNil(t, found)
	assert.Equal(t, event.Name, found.Name)
	assert.Equal(t, event.Message, found.Message)
	assert.Equal(t, event.DeprecatedExtraInfo, found.DeprecatedExtraInfo)
	assert.Equal(t, event.DeprecatedSuggestedActions.DeprecatedDescriptions[0], found.DeprecatedSuggestedActions.DeprecatedDescriptions[0])
}

func TestConcurrentTableCreation(t *testing.T) {
	t.Parallel()

	dbRW, dbRO, cleanup := sqlite.OpenTestDB(t)
	defer cleanup()

	// Try to create multiple stores with the same table name concurrently
	tableName := "concurrent_table"
	concurrency := 10
	var wg sync.WaitGroup
	stores := make([]Store, concurrency)
	errors := make([]error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			store, err := New(dbRW, dbRO, 0)
			assert.NoError(t, err)
			bucket, err := store.Bucket(tableName)
			assert.NoError(t, err)
			defer bucket.Close()

			stores[index] = store
			errors[index] = err
		}(i)
	}

	wg.Wait()

	// Verify that all attempts either succeeded or failed gracefully
	successCount := 0
	for i := 0; i < concurrency; i++ {
		if errors[i] == nil {
			successCount++
			assert.NotNil(t, stores[i])
		}
	}
	assert.Greater(t, successCount, 0)
}

func TestEventTypeValidation(t *testing.T) {
	t.Parallel()

	testTableName := "test_table"

	dbRW, dbRO, cleanup := sqlite.OpenTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	store, err := New(dbRW, dbRO, 0)
	assert.NoError(t, err)
	bucket, err := store.Bucket(testTableName)
	assert.NoError(t, err)
	defer bucket.Close()

	// Test all valid event types
	validTypes := []apiv1.EventType{
		apiv1.EventTypeWarning,
		apiv1.EventTypeInfo,
		apiv1.EventTypeCritical,
		apiv1.EventTypeFatal,
		apiv1.EventTypeUnknown,
	}

	baseTime := time.Now().UTC()
	for i, eventType := range validTypes {
		event := apiv1.Event{
			Time:    metav1.Time{Time: baseTime.Add(time.Duration(i) * time.Second)},
			Name:    "test",
			Type:    eventType,
			Message: fmt.Sprintf("Test message for %s", eventType),
		}

		err = bucket.Insert(ctx, event)
		assert.NoError(t, err)

		found, err := bucket.Find(ctx, event)
		assert.NoError(t, err)
		assert.NotNil(t, found)
		assert.Equal(t, eventType, found.Type)
	}

	// Verify all events can be retrieved
	events, err := bucket.Get(ctx, baseTime.Add(-time.Hour))
	assert.NoError(t, err)
	assert.Equal(t, len(validTypes), len(events))
}

func TestRetentionPurge(t *testing.T) {
	t.Parallel()

	testTableName := "test_table"

	dbRW, dbRO, cleanup := sqlite.OpenTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	bucket, err := newTable(
		dbRW,
		dbRO,
		testTableName,
		10*time.Second,
		// much shorter than the retention period
		// to make tests less flaky
		50*time.Millisecond,
	)
	assert.NoError(t, err)
	defer bucket.Close()

	baseTime := time.Now().UTC()
	events := apiv1.Events{
		{
			Time:                metav1.Time{Time: baseTime.Add(-15 * time.Second)},
			Name:                "test",
			Type:                apiv1.EventTypeWarning,
			DeprecatedExtraInfo: map[string]string{"id": "old_event"},
			DeprecatedSuggestedActions: &apiv1.SuggestedActions{
				DeprecatedDescriptions: []string{"old event"},
			},
		},
		{
			Time:                metav1.Time{Time: baseTime.Add(-5 * time.Second)},
			Name:                "test",
			Type:                apiv1.EventTypeWarning,
			DeprecatedExtraInfo: map[string]string{"id": "new_event"},
			DeprecatedSuggestedActions: &apiv1.SuggestedActions{
				DeprecatedDescriptions: []string{"new event"},
			},
		},
	}

	for _, event := range events {
		err = bucket.Insert(ctx, event)
		assert.NoError(t, err)
	}

	time.Sleep(time.Second)

	remaining, err := bucket.Get(ctx, baseTime.Add(-20*time.Second))
	assert.NoError(t, err)
	assert.Equal(t, 1, len(remaining))
	assert.Equal(t, "new_event", remaining[0].DeprecatedExtraInfo["id"])
}

func TestLatest(t *testing.T) {
	t.Parallel()

	testTableName := "test_table"

	dbRW, dbRO, cleanup := sqlite.OpenTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	store, err := New(dbRW, dbRO, 0)
	assert.NoError(t, err)
	bucket, err := store.Bucket(testTableName)
	assert.NoError(t, err)
	defer bucket.Close()

	// Test with empty store
	latestEvent, err := bucket.Latest(ctx)
	assert.NoError(t, err)
	assert.Nil(t, latestEvent, "Latest should return nil for empty store")

	// Insert events with different timestamps
	baseTime := time.Now().UTC()
	events := apiv1.Events{
		{
			Time:    metav1.Time{Time: baseTime.Add(-10 * time.Second)},
			Name:    "test",
			Type:    apiv1.EventTypeWarning,
			Message: "old event",
			DeprecatedExtraInfo: map[string]string{
				"id": "event1",
			},
			DeprecatedSuggestedActions: &apiv1.SuggestedActions{
				DeprecatedDescriptions: []string{"old event action"},
			},
		},
		{
			Time:    metav1.Time{Time: baseTime},
			Name:    "test",
			Type:    apiv1.EventTypeInfo,
			Message: "latest event",
			DeprecatedExtraInfo: map[string]string{
				"id": "event2",
			},
			DeprecatedSuggestedActions: &apiv1.SuggestedActions{
				DeprecatedDescriptions: []string{"latest event action"},
			},
		},
		{
			Time:    metav1.Time{Time: baseTime.Add(-5 * time.Second)},
			Name:    "test",
			Type:    apiv1.EventTypeCritical,
			Message: "middle event",
			DeprecatedExtraInfo: map[string]string{
				"id": "event3",
			},
			DeprecatedSuggestedActions: &apiv1.SuggestedActions{
				DeprecatedDescriptions: []string{"middle event action"},
			},
		},
	}

	// Insert events in random order
	for _, event := range events {
		err = bucket.Insert(ctx, event)
		assert.NoError(t, err)
	}

	// Get latest event
	latestEvent, err = bucket.Latest(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, latestEvent)

	// Verify it's the event with the most recent timestamp
	assert.Equal(t, baseTime.Unix(), latestEvent.Time.Unix())
	assert.Equal(t, "latest event", latestEvent.Message)
	assert.Equal(t, apiv1.EventTypeInfo, latestEvent.Type)
	assert.Equal(t, "event2", latestEvent.DeprecatedExtraInfo["id"])
	assert.Equal(t, "latest event action", latestEvent.DeprecatedSuggestedActions.DeprecatedDescriptions[0])

	// Test with canceled context
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately
	_, err = bucket.Latest(canceledCtx)
	assert.Error(t, err)

	// Test after purging all events
	deleted, err := bucket.Purge(ctx, baseTime.Add(time.Hour).Unix())
	assert.NoError(t, err)
	assert.Equal(t, 3, deleted)

	latestEvent, err = bucket.Latest(ctx)
	assert.NoError(t, err)
	assert.Nil(t, latestEvent, "Latest should return nil after purging all events")
}

func TestCompareEvent(t *testing.T) {
	tests := []struct {
		name     string
		eventA   apiv1.Event
		eventB   apiv1.Event
		expected bool
	}{
		{
			name: "same events",
			eventA: apiv1.Event{
				DeprecatedExtraInfo: map[string]string{
					"key1": "value1",
					"key2": "value2",
				},
			},
			eventB: apiv1.Event{
				DeprecatedExtraInfo: map[string]string{
					"key1": "value1",
					"key2": "value2",
				},
			},
			expected: true,
		},
		{
			name: "different key-value pairs",
			eventA: apiv1.Event{
				DeprecatedExtraInfo: map[string]string{
					"key1": "value1",
					"key2": "value2",
				},
			},
			eventB: apiv1.Event{
				DeprecatedExtraInfo: map[string]string{
					"key1": "value1",
					"key2": "different_value",
				},
			},
			expected: false,
		},
		{
			name: "eventB missing key",
			eventA: apiv1.Event{
				DeprecatedExtraInfo: map[string]string{
					"key1": "value1",
					"key2": "value2",
				},
			},
			eventB: apiv1.Event{
				DeprecatedExtraInfo: map[string]string{
					"key1": "value1",
				},
			},
			expected: false,
		},
		{
			name: "eventA missing key",
			eventA: apiv1.Event{
				DeprecatedExtraInfo: map[string]string{
					"key1": "value1",
				},
			},
			eventB: apiv1.Event{
				DeprecatedExtraInfo: map[string]string{
					"key1": "value1",
					"key2": "value2",
				},
			},
			expected: false,
		},
		{
			name: "empty events",
			eventA: apiv1.Event{
				DeprecatedExtraInfo: map[string]string{},
			},
			eventB: apiv1.Event{
				DeprecatedExtraInfo: map[string]string{},
			},
			expected: true,
		},
		{
			name: "one empty event",
			eventA: apiv1.Event{
				DeprecatedExtraInfo: map[string]string{},
			},
			eventB: apiv1.Event{
				DeprecatedExtraInfo: map[string]string{
					"key1": "value1",
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := compareEvent(tt.eventA, tt.eventB)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestUnmarshalIfValid(t *testing.T) {
	t.Parallel()

	type testStruct struct {
		Key   string `json:"key"`
		Value int    `json:"value"`
	}

	tests := []struct {
		name          string
		data          sql.NullString
		expectedError bool
		expectedObj   *testStruct
	}{
		{
			name:          "invalid SQL string",
			data:          sql.NullString{Valid: false},
			expectedError: false,
			expectedObj:   nil,
		},
		{
			name:          "empty string",
			data:          sql.NullString{String: "", Valid: true},
			expectedError: false,
			expectedObj:   nil,
		},
		{
			name:          "null string",
			data:          sql.NullString{String: "null", Valid: true},
			expectedError: false,
			expectedObj:   nil,
		},
		{
			name:          "not starting with {",
			data:          sql.NullString{String: "[1, 2, 3]", Valid: true},
			expectedError: true,
			expectedObj:   nil,
		},
		{
			name:          "valid JSON",
			data:          sql.NullString{String: `{"key":"test","value":123}`, Valid: true},
			expectedError: false,
			expectedObj:   &testStruct{Key: "test", Value: 123},
		},
		{
			name:          "invalid JSON format",
			data:          sql.NullString{String: `{"key":"test","value":"not-an-int"}`, Valid: true},
			expectedError: true,
			expectedObj:   nil,
		},
		{
			name:          "malformed JSON",
			data:          sql.NullString{String: `{"key":"test", unclosed}`, Valid: true},
			expectedError: true,
			expectedObj:   nil,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var result *testStruct
			if tt.expectedObj != nil {
				result = &testStruct{}
			}

			err := unmarshalIfValid(tt.data, &result)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			if tt.expectedObj != nil {
				assert.Equal(t, tt.expectedObj, result)
			} else if !tt.expectedError {
				assert.Nil(t, result)
			}
		})
	}
}
