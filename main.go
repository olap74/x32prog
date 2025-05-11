package main

import (
    "flag"
    "fmt"
    "io"
    "log"
    "net"
    "os"
    "time"

    "github.com/hypebeast/go-osc/osc"
)

var (
    x32IP        string
    x32Port      int
    localIP      string
    verbosity    int
    muteGroup    = "/config/mute/2"
    pollInterval = 500 * time.Millisecond
    lastMuteState float32 = -1
)

func main() {
    // Define command-line flags
    flag.StringVar(&x32IP, "x32IP", "192.168.100.235", "IP address of the X32 mixer")
    flag.IntVar(&x32Port, "x32Port", 10023, "Port of the X32 mixer")
    flag.StringVar(&localIP, "localIP", "192.168.100.201", "Local IP address to bind to")
    flag.IntVar(&verbosity, "verbosity", 0, "Verbosity level (0: no logs, 1: received OSC messages only, 2: all logs)")
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

    for {
        msg := osc.NewMessage(muteGroup)
        sendOSCMessage(conn, msg) // Use the existing UDP connection for sending
        time.Sleep(pollInterval)
    }
}

func listenForResponses(conn *net.UDPConn) {
    dispatcher := osc.NewStandardDispatcher()

    dispatcher.AddMsgHandler(muteGroup, func(msg *osc.Message) {
        if len(msg.Arguments) == 0 {
            if verbosity == 2 {
                log.Println("No arguments in OSC message")
            }
            return
        }

        if verbosity >= 1 {
            log.Printf("Received OSC message: %s %v", msg.Address, msg.Arguments)
        }

        var state float32
        switch v := msg.Arguments[0].(type) {
        case int32:
            state = float32(v) // Convert int32 to float32
        case float32:
            state = v
        default:
            if verbosity == 2 {
                log.Printf("Invalid mute state type: %T", msg.Arguments[0]) // Log the actual type
            }
            return
        }

        if state == 0 {
            if verbosity == 2 {
                log.Println("Mute group 2 is OFF, muting channels 29 & 30")
            }
            updateMuteChannels(true, []int{29, 30}, conn) // Mute channels 29 & 30
        } else if state == 1 {
            if verbosity == 2 {
                log.Println("Mute group 2 is ON, unmuting channels 29 & 30")
            }
            updateMuteChannels(false, []int{29, 30}, conn) // Unmute channels 29 & 30
        }
    })

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

        packet, err := osc.ParsePacket(string(buf[:n])) // Use buf[:n] directly as []byte
        if err != nil {
            if verbosity == 2 {
                log.Printf("Failed to parse OSC packet: %v", err)
            }
            continue
        }

        dispatcher.Dispatch(packet) // Dispatch the parsed packet
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

func updateMuteChannels(mute bool, channels []int, conn *net.UDPConn) {
    state := float32(1) // Default to unmute
    if mute {
        state = 0 // Set to mute
    }

    if lastMuteState == state {
        log.Printf("Mute state unchanged for channels: %v, State: %v", channels, state)
        return
    }

    lastMuteState = state
    log.Printf("Updating mute state for channels: %v, State: %v", channels, state)

    for _, ch := range channels {
        path := fmt.Sprintf("/ch/%02d/mix/on", ch) // Correct OSC path for X32 mixer
        msg := osc.NewMessage(path)
        msg.Append(state)

        // Log the exact command being prepared
        log.Printf("Preparing to send OSC command: Path=%s, State=%v", path, state)

        sendOSCMessage(conn, msg) // Use the existing UDP connection for sending
        time.Sleep(10 * time.Millisecond) // Small delay for reliability
    }

    // Additional operation for channel 15 gate key source
    gateKeySourcePath := "/ch/15/gate/keysrc"
    var gateKeySource int32 = 59 // Default to 59 (Bus 11) when muting
    if !mute {
        gateKeySource = 29 // Set to 29 (Ch 29) when unmuting
    }

    msg := osc.NewMessage(gateKeySourcePath)
    msg.Append(gateKeySource)

    // Log the gate key source command being prepared
    log.Printf("Preparing to send OSC command for gate key source: Path=%s, Key Source=%d", gateKeySourcePath, gateKeySource)

    sendOSCMessage(conn, msg) // Send the gate key source update

    // Send additional parameters based on gateKeySource
    var holdValue, releaseValue float32
    if gateKeySource == 59 {
        holdValue = 0.99999
        releaseValue = 0.99999
    } else if gateKeySource == 29 {
        holdValue = 0.8
        releaseValue = 0.7
    }

    // Send /15/gate/hold
    holdPath := "/ch/15/gate/hold"
    holdMsg := osc.NewMessage(holdPath)
    holdMsg.Append(holdValue)
    log.Printf("Preparing to send OSC command: Path=%s, Value=%f", holdPath, holdValue)
    sendOSCMessage(conn, holdMsg)

    // Send /15/gate/release
    releasePath := "/ch/15/gate/release"
    releaseMsg := osc.NewMessage(releasePath)
    releaseMsg.Append(releaseValue)
    log.Printf("Preparing to send OSC command: Path=%s, Value=%f", releasePath, releaseValue)
    sendOSCMessage(conn, releaseMsg)

    // Log confirmation that the additional parameters were sent
    log.Printf("Additional parameters sent: Hold=%f, Release=%f", holdValue, releaseValue)
}