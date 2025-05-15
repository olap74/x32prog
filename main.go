package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"time"

	"io/ioutil"
	"sync"

	"github.com/hypebeast/go-osc/osc"
	"gopkg.in/yaml.v3"
)

var (
	x32IP         string
	x32Port       int
	localIP       string
	verbosity     int
	configPath    string
	config        *Config
	paramStates   = make(map[string]interface{})
	paramStatesMu sync.Mutex
	pollInterval  = 500 * time.Millisecond
)

// Config structs for YAML
type Config struct {
	WatchOn []WatchOnEntry `yaml:"watch_on"`
	Set     []SetEntry     `yaml:"set"`
}

type WatchOnEntry struct {
	Parameter string        `yaml:"parameter"`
	Type      string        `yaml:"type"`
	Actions   []ActionEntry `yaml:"actions"`
}

type ActionEntry struct {
	Value interface{} `yaml:"value"`
	Set   []SetEntry  `yaml:"set"`
}

type SetEntry struct {
	Path  string      `yaml:"path"`
	Type  string      `yaml:"type"`
	Value interface{} `yaml:"value"`
}

func main() {
	// Define command-line flags
	flag.StringVar(&x32IP, "x32IP", "192.168.56.3", "IP address of the X32 mixer")
	flag.IntVar(&x32Port, "x32Port", 10023, "Port of the X32 mixer")
	flag.StringVar(&localIP, "localIP", "192.168.56.1", "Local IP address to bind to")
	flag.IntVar(&verbosity, "verbosity", 0, "Verbosity level (0: no logs, 1: received OSC messages only, 2: all logs)")
	flag.StringVar(&configPath, "config", "pipeline.yaml", "Path to YAML config file")
	showHelp := flag.Bool("help", false, "Show help message")

	flag.Parse()

	// Display current parameter values
	fmt.Println("Current parameters:")
	fmt.Printf("  x32IP: %s\n", x32IP)
	fmt.Printf("  x32Port: %d\n", x32Port)
	fmt.Printf("  localIP: %s\n", localIP)
	fmt.Printf("  verbosity: %d\n", verbosity)

	// Show help and exit if the help flag is set
	if *showHelp {
		flag.Usage()
		os.Exit(0)
	}

	// Load config
	var err error
	config, err = loadConfig(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Configure logging based on verbosity level
	if verbosity == 0 {
		log.SetOutput(io.Discard) // Disable logging
	}

	localAddr := &net.UDPAddr{
		IP:   net.ParseIP(localIP),
		Port: 0,
	}

	conn, err := net.ListenUDP("udp", localAddr)
	if err != nil {
		log.Fatalf("Failed to bind to local UDP port: %v", err)
	}
	defer conn.Close()
	log.Println("Listening on", conn.LocalAddr())

	go listenForResponses(conn)

	// Главный цикл: проверка связи, опрос и установка параметров
	for {
		if !pingX32(conn) {
			log.Println("No connection to X32 mixer, retrying in 2s...")
			time.Sleep(2 * time.Second)
			continue
		}

		// Опрос всех watch_on параметров
		for _, watch := range config.WatchOn {
			pollWatchedParameterOnce(watch, conn)
		}

		// Принудительная установка всех set параметров
		enforceSetParameters(conn)

		time.Sleep(500 * time.Millisecond)
	}
}

// Load YAML config
func loadConfig(path string) (*Config, error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Проверка связи с X32 через OSC /status
func pingX32(conn *net.UDPConn) bool {
	msg := osc.NewMessage("/status")
	sendOSCMessage(conn, msg)
	// Можно реализовать ожидание ответа, но для простоты считаем, что отправка удалась
	return true
}

// Опрос одного параметра (разово)
func pollWatchedParameterOnce(watch WatchOnEntry, conn *net.UDPConn) {
	param := watch.Parameter
	msg := osc.NewMessage(param)
	sendOSCMessage(conn, msg)
}

// Enforce 'set' parameters from config
func enforceSetParameters(conn *net.UDPConn) {
	for _, set := range config.Set {
		sendSetOSC(conn, set)
	}
}

// Send a set command as OSC
func sendSetOSC(conn *net.UDPConn, set SetEntry) {
	msg := osc.NewMessage(set.Path)
	switch set.Type {
	case "int32":
		v, _ := toInt32(set.Value)
		msg.Append(v)
	case "float32":
		v, _ := toFloat32(set.Value)
		msg.Append(v)
	default:
		return
	}
	sendOSCMessage(conn, msg)
}

// Helper to convert interface{} to int32
func toInt32(val interface{}) (int32, bool) {
	switch v := val.(type) {
	case int:
		return int32(v), true
	case int32:
		return v, true
	case float64:
		return int32(v), true
	case float32:
		return int32(v), true
	}
	return 0, false
}

// Helper to convert interface{} to float32
func toFloat32(val interface{}) (float32, bool) {
	switch v := val.(type) {
	case float32:
		return v, true
	case float64:
		return float32(v), true
	case int:
		return float32(v), true
	case int32:
		return float32(v), true
	}
	return 0, false
}

func listenForResponses(conn *net.UDPConn) {
	dispatcher := osc.NewStandardDispatcher()

	// Register handlers for all watched parameters
	for _, watch := range config.WatchOn {
		param := watch.Parameter
		dispatcher.AddMsgHandler(param, makeWatchHandler(watch, conn))
	}

	dispatcher.AddMsgHandler("*", func(msg *osc.Message) {
		if verbosity == 2 {
			log.Printf("Received OSC message: %s %v", msg.Address, msg.Arguments)
		}
	})

	buf := make([]byte, 1024)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			if verbosity == 2 {
				log.Printf("Error reading from socket: %v", err)
			}
			continue
		}

		packet, err := osc.ParsePacket(string(buf[:n]))
		if err != nil {
			if verbosity == 2 {
				log.Printf("Failed to parse OSC packet: %v", err)
			}
			continue
		}

		dispatcher.Dispatch(packet)
	}
}

// Handler for watched parameter responses
func makeWatchHandler(watch WatchOnEntry, conn *net.UDPConn) func(msg *osc.Message) {
	return func(msg *osc.Message) {
		if len(msg.Arguments) == 0 {
			if verbosity == 2 {
				log.Println("No arguments in OSC message")
			}
			return
		}

		var val interface{}
		switch watch.Type {
		case "int32":
			switch v := msg.Arguments[0].(type) {
			case int32:
				val = v
			case float32:
				val = int32(v)
			case float64:
				val = int32(v)
			}
		case "float32":
			switch v := msg.Arguments[0].(type) {
			case float32:
				val = v
			case float64:
				val = float32(v)
			case int32:
				val = float32(v)
			}
		default:
			val = msg.Arguments[0]
		}

		// Check if value changed
		paramStatesMu.Lock()
		prev, ok := paramStates[watch.Parameter]
		if ok && prev == val {
			paramStatesMu.Unlock()
			return
		}
		paramStates[watch.Parameter] = val
		paramStatesMu.Unlock()

		if verbosity >= 1 {
			log.Printf("Received OSC message: %s %v", msg.Address, msg.Arguments)
		}

		// Find matching action
		for _, action := range watch.Actions {
			match := false
			switch watch.Type {
			case "int32":
				v, _ := toInt32(action.Value)
				if vi, ok := val.(int32); ok && vi == v {
					match = true
				}
			case "float32":
				v, _ := toFloat32(action.Value)
				if vf, ok := val.(float32); ok && vf == v {
					match = true
				}
			default:
				if val == action.Value {
					match = true
				}
			}
			if match {
				for _, set := range action.Set {
					sendSetOSC(conn, set)
					time.Sleep(10 * time.Millisecond)
				}
				break
			}
		}
	}
}

func sendOSCMessage(conn *net.UDPConn, msg *osc.Message) {
	data, err := msg.MarshalBinary()
	if err != nil {
		if verbosity == 2 {
			log.Printf("Failed to marshal OSC message: %v", err)
		}
		return
	}

	if verbosity == 2 {
		log.Printf("Sending OSC message: Path=%s, Arguments=%v", msg.Address, msg.Arguments)
		log.Printf("Raw OSC data: %x", data) // Log raw data in hexadecimal format for debugging
	}

	_, err = conn.WriteToUDP(data, &net.UDPAddr{IP: net.ParseIP(x32IP), Port: x32Port})
	if err != nil {
		if verbosity == 2 {
			log.Printf("Failed to send OSC message: %v", err)
		}
	} else if verbosity == 2 {
		log.Printf("OSC message successfully sent to %s:%d", x32IP, x32Port)
	}
}
