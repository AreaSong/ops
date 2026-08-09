package runner

import "sync"

type Broker struct {
	mu          sync.Mutex
	nextID      int
	subscribers map[int]chan int64
}

func NewBroker() *Broker {
	return &Broker{subscribers: make(map[int]chan int64)}
}

func (broker *Broker) Subscribe() (<-chan int64, func()) {
	broker.mu.Lock()
	id := broker.nextID
	broker.nextID++
	channel := make(chan int64, 1)
	broker.subscribers[id] = channel
	broker.mu.Unlock()
	return channel, func() {
		broker.mu.Lock()
		delete(broker.subscribers, id)
		broker.mu.Unlock()
	}
}

func (broker *Broker) Publish(sequence int64) {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	for _, channel := range broker.subscribers {
		select {
		case channel <- sequence:
		default:
		}
	}
}
