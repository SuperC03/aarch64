package main

import (
	"context"
	"flag"
	"net"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/bwmarrin/snowflake"
	"github.com/digitalocean/go-libvirt"
	"github.com/fosshostorg/aarch64/daemons/hydrogen/utils"
	"github.com/fosshostorg/aarch64/daemons/internal/commons"
	"github.com/fosshostorg/aarch64/daemons/internal/message"
	jsoniter "github.com/json-iterator/go"
	"github.com/nsqio/go-nsq"
	"go.uber.org/zap"
)

var json = jsoniter.ConfigCompatibleWithStandardLibrary

func NewNSQHandler(l *zap.Logger, p *nsq.Producer, virt *libvirt.Libvirt, sfn *snowflake.Node, domainCachePath string) *NSQHandler {
	return &NSQHandler{
		l:               l,
		p:               p,
		virt:            virt,
		sfn:             sfn,
		domainCachePath: domainCachePath,
		data:            make(map[string]message.VMData),
		seenIds:         make(map[int64]bool),
		mutex:           sync.Mutex{},
	}
}

type NSQHandler struct {
	l               *zap.Logger
	p               *nsq.Producer
	virt            *libvirt.Libvirt
	sfn             *snowflake.Node
	domainCachePath string
	data            map[string]message.VMData
	seenIds         map[int64]bool
	mutex           sync.Mutex
}

func (h *NSQHandler) HandleMessage(m *nsq.Message) error {
	// Message Body Should Not be Empty
	if m.Body == nil || len(m.Body) <= 0 {
		h.l.Error("message body is empty")
		return nil
	}

	// Decode Message Body
	var msg message.Message
	if err := json.Unmarshal(m.Body, &msg); err != nil {
		h.l.Error("failed to unmarshal message", zap.Error(err))
		return nil
	}

	// h.mutex.Lock()
	// defer h.mutex.Unlock()
	// Ensure Duplicate Messaages are Deleted
	if h.seenIds[msg.ID] {
		h.l.Error("duplicate message", zap.Int64("message_id", msg.ID))
		return nil
	}
	h.seenIds[msg.ID] = true

	// Handle Message Actions
	switch msg.Action {
	// TODO Add and Remove Domains
	case message.ChangeState:
		msgData := &msg.MessageData
		h.changeDomainState(msgData)
	case message.AddDomain:
		vmData := &msg.VMData
		h.addDomain(vmData)
	case message.DeleteDomain:
		vmData := &msg.VMData
		h.deleteDomain(vmData)
	}

	return nil
}

func (h *NSQHandler) SaveDomainCache() error {
	file, err := os.Create(h.domainCachePath)
	defer file.Close()
	if err != nil {
		h.l.Error("failed to create hydrogen.json", zap.Error(err))
		return nil
	}
	encoder := json.NewEncoder(file)
	if err = encoder.Encode(h.data); err != nil {
		h.l.Error("failed to save hydrogen.json", zap.Error(err))
		return nil
	}
	return nil
}

func (h *NSQHandler) LoadDomainCache() error {
	file, err := os.Open(h.domainCachePath)
	if err != nil {
		h.l.Info("unable to open hydrogen.json")
		return nil
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	if err = decoder.Decode(&h.data); err != nil {
		h.l.Error("failed to load hydrogen.json")
		h.data = make(map[string]message.VMData)
		return nil
	}
	var (
		total       int = 0
		bridgeCount int = 0
		domainCount int = 0
	)
	for _, v := range h.data {
		total += 1
		if err := utils.CreateAndStartBridge(h.l, &v); err == nil {
			bridgeCount += 1
		}
		if err := utils.CreateDomain(h.l, &v); err == nil {
			domainCount += 1
		}
	}
	h.l.Info(
		"Successfully Loaded hydrogen.json",
		zap.Int("bridges_created", bridgeCount),
		zap.Int("domains_created", domainCount),
	)
	return nil
}

func (h *NSQHandler) MonitorDomainStatus(ctx context.Context) error {
	events, err := h.virt.LifecycleEvents(ctx)
	if err != nil {
		h.l.Error("unable to monitor domain status", zap.Error(err))
		return nil
	}
	for e := range events {
		switch libvirt.DomainEventType(e.Event) {
		case libvirt.DomainEventStarted:
			msg := message.Message{
				ID:     int64(h.sfn.Generate()),
				Action: message.NewVMState,
				MessageData: message.MessageData{
					Name: e.Dom.Name,
					Num:  1,
				},
			}
			commons.ProducerSendStruct(msg, "aarch64-power", h.p)
		case libvirt.DomainEventStopped:
			msg := message.Message{
				ID:     int64(h.sfn.Generate()),
				Action: message.NewVMState,
				MessageData: message.MessageData{
					Name: e.Dom.Name,
					Num:  5,
				},
			}
			commons.ProducerSendStruct(msg, "aarch64-power", h.p)
		}
	}
	return nil
}

func (h *NSQHandler) addDomain(data *message.VMData) error {
	if err := utils.CreateAndStartBridge(h.l, data); err != nil {
		return nil
	}
	if err := utils.CreateDomain(h.l, data); err != nil {
		return nil
	}

	// Update In-Memory Storage and Cache
	h.data[data.ID] = *data
	h.SaveDomainCache()
	return nil
}

func (h *NSQHandler) deleteDomain(data *message.VMData) error {
	utils.DeleteDomain(h.l, data)
	utils.DeleteBridge(h.l, data)

	delete(h.data, data.ID)
	h.SaveDomainCache()
	return nil
}

func (h *NSQHandler) changeDomainState(data *message.MessageData) error {
	// Locate Domain for Operations
	var domain libvirt.Domain
	if data.Name != "" {
		tDomain, err := h.virt.DomainLookupByName(data.Name)
		if err != nil {
			h.l.Error(
				"unable to locate domain",
				zap.String("name", data.Name),
				zap.Error(err),
			)
			return nil
		}
		domain = tDomain
	}
	// Handle Domain State Changes
	switch data.Event {
	case message.StateShutdown:
		h.virt.DomainShutdown(domain)
	case message.StateReboot:
		h.virt.DomainReboot(domain, libvirt.DomainRebootDefault)
	case message.StateReset:
		h.virt.DomainReset(domain, 0)
	case message.StateStartup:
		h.virt.DomainCreate(domain)
	case message.StateStop:
		h.virt.DomainDestroy(domain)
	default:
		h.l.Error("unknown state change event")
	}
	return nil
}

func main() {
	l, _ := zap.NewDevelopment()

	// Flag Variables
	var (
		nsqConnectURI   string
		domainCachePath string
	)

	// Parse Flags
	flag.StringVar(&nsqConnectURI, "nsq-connect-uri", commons.NSQCoreUrl, "The URI for NSQ producers & consumers to connect to")
	flag.StringVar(&domainCachePath, "domain-cache-path", commons.DomainCachePath, "The path to the domain cache file")
	flag.Parse()

	// Connect to LibVirt
	lvc, err := net.DialTimeout("unix", "/var/run/libvirt/libvirt-sock", 2*time.Second)
	if err != nil {
		l.Fatal("unable to connect to libvirt socket", zap.Error(err))
	}
	lv := libvirt.New(lvc)
	if err := lv.Connect(); err != nil {
		l.Fatal("unable to connect to libvirt socket", zap.Error(err))
	}
	l.Info("Successfully Connected to LibVirt")

	// Create Snowflake Node
	sfNode := commons.GetSnow()

	// Connect to NSQ
	hostname := commons.GetHostname()
	if hostname == "" {
		l.Fatal("failed to read hostname")
	}
	nsqProducer, err := nsq.NewProducer(nsqConnectURI, nsq.NewConfig())
	if err != nil {
		l.Fatal("unable to connect to NSQ", zap.Error(err))
	}
	nh := NewNSQHandler(l, nsqProducer, lv, sfNode, domainCachePath)
	nh.LoadDomainCache()
	nsqConsumer := commons.CreateNSQConsumer(nsqConnectURI, "aarch64-libvirt-"+hostname, "main", nh)
	l.Info(
		"Successfully Connected to NSQ",
		zap.String("topic", "aarch64-libvirt-"+hostname),
		zap.String("channel", "main"),
	)
	defer nsqConsumer.Stop()

	// Start Domain Monitor
	ctx := context.Background()
	go nh.MonitorDomainStatus(ctx)
	defer ctx.Done()

	l.Info("Hydrogen has Started!!!")

	// Handle Shutting Down
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	signal.Notify(c, os.Kill)
	<-c
	l.Info("Hydrogen Shutting Down...Byeeee")
}
