package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/ChimeraCoder/anaconda"
	"github.com/mdlayher/ethernet"
	"github.com/mdlayher/raw"
	"golang.org/x/net/ipv4"
)

func main() {
	ifname := flag.String("ifname", "enp0s8", "interface name")
	trackLabel := flag.String("track-label", "#2_4_4_0_24",
		"from web-avian carrier network to local network")
	destinationLabel := flag.String("destination-label", "#2_4_4_0_24",
		"from local network to web-avian carrier network")
	flag.Parse()

	api := anaconda.NewTwitterApiWithCredentials("", // your-access-token
		"", // your-access-token-secret
		"", // your-consumer-key
		"") //your-consumer-secret

	// read from ethernet device and send packets to web-avian carrier network
	go func() {
		ifi, err := net.InterfaceByName(*ifname)
		if err != nil {
			fmt.Fprintf(os.Stderr, "interface %q: %v", *ifname, err)
			os.Exit(1)
		}

		cfg, err := raw.ListenPacket(ifi, 0x0800, &raw.Config{
			LinuxSockDGRAM: false,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "listen: %v", err)
			os.Exit(1)
		}

		var frame ethernet.Frame
		buf := make([]byte, ((280-len(*destinationLabel)-1)/4)*3)

		for {
			// read from device
			n, addr, err := cfg.ReadFrom(buf)
			if err != nil {
				fmt.Fprintf(os.Stderr, "read: %v\n", err)
				time.Sleep(100 * time.Millisecond)
				continue
			}

			// parse ethernet frame to extract payload
			if err := (&frame).UnmarshalBinary(buf[:n]); err != nil {
				fmt.Fprintf(os.Stderr, "unmarshal: %v\n", err)
				time.Sleep(100 * time.Millisecond)
				continue
			}

			// base64 encode the packet and discard malformed frames (using a
			// stupid but surprisingly effecting method: decode base64)
			encoded := base64.StdEncoding.EncodeToString(frame.Payload)
			_, err = base64.StdEncoding.DecodeString(encoded)
			if err != nil || strings.Contains(encoded, "/") {
				fmt.Fprint(os.Stderr, "received malformed frame\n")
				continue
			}
			tweet := encoded + " " + *destinationLabel
			fmt.Printf("from %v: %v\n", addr.String(), tweet)

			// send to web-avian carrier network
			_, err = api.PostTweet(tweet, url.Values{})
			if err != nil {
				fmt.Fprintf(os.Stderr, "post tweet: %v\n", err)
				time.Sleep(10 * time.Second)
				continue
			}
			// twitter loves us to rate limit API usage
			time.Sleep(750 * time.Millisecond)
		}
	}()

	// open sending socket for local network
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_RAW, syscall.IPPROTO_RAW)
	if err != nil {
		fmt.Fprintf(os.Stderr, "socket: %v\n", err)
		os.Exit(1)
	}

	// start tracking label
	stream := api.PublicStreamFilter(url.Values{"track": []string{*trackLabel}})
	defer stream.Stop()

	for v := range stream.C {
		t, ok := v.(anaconda.Tweet)
		if !ok {
			fmt.Fprintf(os.Stderr, "malformed tweet")
			continue
		}

		encoded := strings.Split(t.FullText, " ")[0]
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(data) < 60 { // 60 = reasonable packet size ;)
			fmt.Fprint(os.Stderr, "received malformed tweet\n")
			continue
		}
		hdr := ipv4.Header{}
		hdr.Parse(data)

		// we should decrease the TTL here...
		// YOLO!

		// create socket address for sendto() syscall
		ip := hdr.Dst.To4()
		addr := syscall.SockaddrInet4{
			Port: 0,
			Addr: [4]byte{ip[0], ip[1], ip[2], ip[3]},
		}

		// make it happen, send the packet!
		fmt.Printf("to %v: %v\n", ip.String(), encoded)
		err = syscall.Sendto(fd, data, 0, &addr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sendto: %v\n", err)
			continue
		}
	}
}
