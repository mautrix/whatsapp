package appservice

import (
	"maunium.net/go/gomatrix"
	log "maunium.net/go/maulogger"
)

type ExecMode uint8

const (
	AsyncHandlers ExecMode = iota
	AsyncLoop
	Sync
)

type EventProcessor struct {
	ExecMode ExecMode

	as       *AppService
	log      log.Logger
	stop     chan struct{}
	handlers map[gomatrix.EventType][]gomatrix.OnEventListener
}

func NewEventProcessor(as *AppService) *EventProcessor {
	return &EventProcessor{
		ExecMode: AsyncHandlers,
		as:       as,
		log:      as.Log.Sub("Events"),
		stop:     make(chan struct{}, 1),
		handlers: make(map[gomatrix.EventType][]gomatrix.OnEventListener),
	}
}

func (ep *EventProcessor) On(evtType gomatrix.EventType, handler gomatrix.OnEventListener) {
	handlers, ok := ep.handlers[evtType]
	if !ok {
		handlers = []gomatrix.OnEventListener{handler}
	} else {
		handlers = append(handlers, handler)
	}
	ep.handlers[evtType] = handlers
}

func (ep *EventProcessor) Start() {
	for {
		select {
		case evt := <-ep.as.Events:
			handlers, ok := ep.handlers[evt.Type]
			if !ok {
				continue
			}
			switch ep.ExecMode {
			case AsyncHandlers:
				for _, handler := range handlers {
					go handler(evt)
				}
			case AsyncLoop:
				go func() {
					for _, handler := range handlers {
						handler(evt)
					}
				}()
			case Sync:
				for _, handler := range handlers {
					handler(evt)
				}
			}
		case <-ep.stop:
			return
		}
	}
}

func (ep *EventProcessor) Stop() {
	ep.stop <- struct{}{}
}
