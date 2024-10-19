package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

// InventoryItem represents an item in the inventory.
type InventoryItem struct {
	ItemID      string
	WarehouseID string
	Count       int
	Holds       map[string]Hold
	Mutex       sync.Mutex
}

// Hold represents a hold placed on an inventory item.
type Hold struct {
	HoldID    string
	Amount    int
	Timestamp time.Time
}

// Event represents an inventory change event for event sourcing.
type Event struct {
	EventID     string
	ItemID      string
	WarehouseID string
	Amount      int
	Timestamp   time.Time
}

// InventoryService manages inventory items.
type InventoryService struct {
	Items  map[string]*InventoryItem
	Events []Event
	Mutex  sync.Mutex
}

// NewInventoryService creates a new InventoryService.
func NewInventoryService() *InventoryService {
	return &InventoryService{
		Items:  make(map[string]*InventoryItem),
		Events: make([]Event, 0),
	}
}

// PlaceHold places a hold on an inventory item.
func (s *InventoryService) PlaceHold(itemID, warehouseID string, amount int) (string, error) {
	s.Mutex.Lock()
	defer s.Mutex.Unlock()

	key := fmt.Sprintf("%s:%s", itemID, warehouseID)
	item, exists := s.Items[key]
	if !exists {
		return "", fmt.Errorf("item not found")
	}

	item.Mutex.Lock()
	defer item.Mutex.Unlock()

	if item.Count < amount {
		return "", fmt.Errorf("insufficient inventory")
	}

	holdID := uuid.New().String()
	hold := Hold{
		HoldID:    holdID,
		Amount:    amount,
		Timestamp: time.Now(),
	}
	item.Count -= amount
	item.Holds[holdID] = hold

	// Record the event
	event := Event{
		EventID:     uuid.New().String(),
		ItemID:      itemID,
		WarehouseID: warehouseID,
		Amount:      -amount,
		Timestamp:   time.Now(),
	}
	s.Events = append(s.Events, event)

	return holdID, nil
}

// ExecuteHold finalizes a hold on an inventory item.
func (s *InventoryService) ExecuteHold(itemID, warehouseID, holdID string) error {
	s.Mutex.Lock()
	defer s.Mutex.Unlock()

	key := fmt.Sprintf("%s:%s", itemID, warehouseID)
	item, exists := s.Items[key]
	if !exists {
		return fmt.Errorf("item not found")
	}

	item.Mutex.Lock()
	defer item.Mutex.Unlock()

	_, exists = item.Holds[holdID]
	if !exists {
		return fmt.Errorf("hold not found")
	}

	// Remove the hold
	delete(item.Holds, holdID)

	// Optionally, log that the hold was executed
	// For this demo, we'll assume the hold was executed successfully

	return nil
}

// ReleaseExpiredHolds releases holds that have expired.
// This can be run periodically to release holds that have not been executed within a certain time frame
// returning the reserved inventory back to the available count.
func (s *InventoryService) ReleaseExpiredHolds(expirationDuration time.Duration) {
	s.Mutex.Lock()
	defer s.Mutex.Unlock()

	now := time.Now()
	for _, item := range s.Items {
		item.Mutex.Lock()
		for holdID, hold := range item.Holds {
			if now.Sub(hold.Timestamp) > expirationDuration {
				// Release the hold
				item.Count += hold.Amount
				delete(item.Holds, holdID)

				// Record the event
				event := Event{
					EventID:     uuid.New().String(),
					ItemID:      item.ItemID,
					WarehouseID: item.WarehouseID,
					Amount:      hold.Amount,
					Timestamp:   time.Now(),
				}
				s.Events = append(s.Events, event)
			}
		}
		item.Mutex.Unlock()
	}
}

// GetInventorySnapshot generates a snapshot of the inventory at a specific timestamp.
func (s *InventoryService) GetInventorySnapshot(at time.Time) map[string]int {
	s.Mutex.Lock()
	defer s.Mutex.Unlock()

	// Initialize snapshot with zero counts
	snapshot := make(map[string]int)
	for key := range s.Items {
		snapshot[key] = 0
	}

	// Replay events up to the specified timestamp
	for _, event := range s.Events {
		if event.Timestamp.After(at) {
			continue
		}
		key := fmt.Sprintf("%s:%s", event.ItemID, event.WarehouseID)
		snapshot[key] += event.Amount
	}

	return snapshot
}

// HTTP Handlers for API endpoints

func (s *InventoryService) getInventorySnapshotHandler(w http.ResponseWriter, r *http.Request) {
	// Parse the 'at' query parameter as a timestamp
	atParam := r.URL.Query().Get("at")
	var atTime time.Time

	if atParam != "" {
		atTimestamp, err := time.Parse(time.RFC3339, atParam)
		if err != nil {
			http.Error(w, "Invalid 'at' timestamp", http.StatusBadRequest)
			return
		}
		atTime = atTimestamp
	} else {
		// If 'at' is not provided, use the current time
		atTime = time.Now()
	}

	snapshot := s.GetInventorySnapshot(atTime)
	json.NewEncoder(w).Encode(snapshot)
}

func (s *InventoryService) placeHoldHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ItemID      string `json:"item_id"`
		WarehouseID string `json:"warehouse_id"`
		Amount      int    `json:"amount"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	holdID, err := s.PlaceHold(req.ItemID, req.WarehouseID, req.Amount)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	resp := map[string]string{"hold_id": holdID}
	json.NewEncoder(w).Encode(resp)
}

func (s *InventoryService) executeHoldHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ItemID      string `json:"item_id"`
		WarehouseID string `json:"warehouse_id"`
		HoldID      string `json:"hold_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	err := s.ExecuteHold(req.ItemID, req.WarehouseID, req.HoldID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Hold executed successfully"))
}

func main() {
	inventoryService := NewInventoryService()

	// Initialize inventory with some items
	inventoryService.Items["ABC123:WH1"] = &InventoryItem{
		ItemID:      "ABC123",
		WarehouseID: "WH1",
		Count:       100,
		Holds:       make(map[string]Hold),
	}

	inventoryService.Items["DEF456:WH2"] = &InventoryItem{
		ItemID:      "DEF456",
		WarehouseID: "WH2",
		Count:       200,
		Holds:       make(map[string]Hold),
	}

	// Initialize inventory with initial events
	ts, err := time.Parse(time.RFC3339, "2023-10-01T15:09:59Z")
	if err != nil {
		fmt.Println("Error parsing timestamp:", err)
		return
	}
	inventoryService.Events = append(inventoryService.Events, Event{
		EventID:     "initial-event-1",
		ItemID:      "ABC123",
		WarehouseID: "WH1",
		Amount:      100,
		Timestamp:   ts,
	})

	inventoryService.Events = append(inventoryService.Events, Event{
		EventID:     "initial-event-2",
		ItemID:      "DEF456",
		WarehouseID: "WH2",
		Amount:      200,
		Timestamp:   ts,
	})

	// Set up HTTP routes
	http.HandleFunc("/place_hold", inventoryService.placeHoldHandler)
	http.HandleFunc("/execute_hold", inventoryService.executeHoldHandler)
	http.HandleFunc("/inventory_snapshot", inventoryService.getInventorySnapshotHandler)

	fmt.Println("Inventory service is running on port 8080")
	http.ListenAndServe(":8080", nil)
}
