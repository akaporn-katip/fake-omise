package webhook

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/akaporn-katip/fake-omise/internal/omise"
	"github.com/akaporn-katip/fake-omise/internal/store"
)

// Dispatcher delivers events to a merchant webhook endpoint, the way Omise
// does: a JSON POST, retried a few times, with every attempt recorded.
type Dispatcher struct {
	store  *store.Store
	url    string
	logger *log.Logger
	client *http.Client

	queue chan string
	wg    sync.WaitGroup
}

// NewDispatcher starts a delivery worker. An empty url disables delivery but
// events are still persisted.
func NewDispatcher(st *store.Store, url string, logger *log.Logger) *Dispatcher {
	d := &Dispatcher{
		store:  st,
		url:    url,
		logger: logger,
		client: &http.Client{Timeout: 5 * time.Second},
		queue:  make(chan string, 256),
	}
	d.wg.Add(1)
	go d.work()
	return d
}

// Enqueue schedules an event for delivery by identifier. It never blocks the
// HTTP handler, and it never shares the handler's object pointers with the
// delivery goroutine: the event is re-read from storage when it is sent.
func (d *Dispatcher) Enqueue(eventID string) {
	if d.url == "" || eventID == "" {
		return
	}
	select {
	case d.queue <- eventID:
	default:
		d.logger.Printf("webhook queue full, dropping event %s", eventID)
	}
}

// Wait blocks until the queue drains or the timeout elapses.
func (d *Dispatcher) Wait(timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		for len(d.queue) > 0 {
			time.Sleep(5 * time.Millisecond)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

func (d *Dispatcher) work() {
	defer d.wg.Done()
	for eventID := range d.queue {
		ev, err := d.store.GetEvent(eventID)
		if err != nil {
			d.logger.Printf("load event %s: %v", eventID, err)
			continue
		}
		d.deliver(ev)
	}
}

func (d *Dispatcher) deliver(ev *omise.Event) {
	payload, err := json.Marshal(ev)
	if err != nil {
		d.logger.Printf("marshal event %s: %v", ev.ID, err)
		return
	}

	backoff := 200 * time.Millisecond
	for attempt := 1; attempt <= 3; attempt++ {
		status, errMsg := d.post(payload)
		delivery := omise.WebhookDelivery{
			Object:    "webhook_delivery",
			ID:        omise.NewID("whdl"),
			URI:       d.url,
			Status:    status,
			Key:       ev.Key,
			Error:     errMsg,
			Payload:   string(payload),
			CreatedAt: omise.NewTimestamp(time.Now()),
		}
		if err := d.store.RecordDelivery(ev.ID, delivery); err != nil {
			d.logger.Printf("record delivery: %v", err)
		}
		if status >= 200 && status < 300 {
			return
		}
		time.Sleep(backoff)
		backoff *= 3
	}
}

func (d *Dispatcher) post(payload []byte) (int, string) {
	req, err := http.NewRequest(http.MethodPost, d.url, bytes.NewReader(payload))
	if err != nil {
		return 0, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "fake-omise")
	resp, err := d.client.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.StatusCode, ""
	}
	return resp.StatusCode, resp.Status
}
