package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	zmq "github.com/pebbe/zmq4"
	"go.nanomsg.org/mangos/v3"
	"go.nanomsg.org/mangos/v3/protocol"
	"go.nanomsg.org/mangos/v3/protocol/rep"
	"go.nanomsg.org/mangos/v3/protocol/req"
	"net"
	"net/http"
	"nhooyr.io/websocket"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	// register transports
	_ "go.nanomsg.org/mangos/v3/transport/all"
)

const (
	zmqPort       = 10000
	mangosPort    = 10001
	websocketPort = 10002
)

func main() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	serverTypePtr := flag.String("type", "pinger", "pinger or ponger")
	endpointPtr := flag.String("to", "", "the address of the target")
	flag.Parse()
	isPinger := *serverTypePtr == "pinger"
	endpoint := *endpointPtr
	if isPinger && endpoint == "" {
		panic("-to not defined")
	}

	var wg sync.WaitGroup
	go startWebsockets(isPinger, endpoint, wg)
	go startMangos(isPinger, endpoint, wg)
	go startZmq(isPinger, endpoint, wg)
	<-signals
	wg.Wait()
}

type pingServer struct{}

func (s *pingServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		panic(err)
	}
	defer c.Close(websocket.StatusInternalError, "whatever")

	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, msg, err := c.Read(ctx)
		if err != nil {
			cancel()
			println(err.Error())
			return
		}
		strMsg := string(msg)
		if strMsg != "ping?" {
			cancel()
			panic(fmt.Sprintf("non-ping message '%s'", strMsg))
		}
		c.Write(ctx, websocket.MessageText, []byte("pong!"))
		println("ponged")
		cancel()
	}
}

func printFLn(f string, v ...any) {
	fmt.Printf(f+"\n", v...)
}

func startWebsockets(isPinger bool, endpoint string, wg sync.WaitGroup) {
	wg.Add(1)
	defer wg.Done()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	var l net.Listener
	var err error
	if isPinger {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		c, _, err := websocket.Dial(ctx, fmt.Sprintf("ws://%s:%d", endpoint, websocketPort), nil)
		if err != nil {
			panic(err)
		}
		go func() {
			for {
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				start := time.Now()
				err = c.Write(ctx, websocket.MessageText, []byte("ping?"))
				if err != nil {
					panic(err)
				}
				_, msg, err := c.Read(ctx)
				if err != nil {
					panic(err)
				}
				strMsg := string(msg)
				if strMsg != "pong!" {
					panic(fmt.Sprintf("non-pong message '%s'", strMsg))
				}
				elapsed := time.Now().Sub(start)
				println(fmt.Sprintf("websocket round-trip took %dns (%dms)", elapsed.Nanoseconds(), elapsed.Milliseconds()))
				time.Sleep(time.Second)
				cancel()
			}
		}()
		<-signals
		c.Close(websocket.StatusGoingAway, "bye bye")
	} else {
		l, err = net.Listen("tcp", fmt.Sprintf(":%d", websocketPort))
		if err != nil {
			panic(err)
		}
		printFLn("Listening to websockets on port %d", websocketPort)
		s := &http.Server{
			Handler:      &pingServer{},
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 10 * time.Second,
		}
		go s.Serve(l)
		<-signals
		s.Shutdown(context.Background())
	}
}

func startMangos(isPinger bool, endpoint string, wg sync.WaitGroup) {
	wg.Add(1)
	defer wg.Done()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	var sock protocol.Socket
	var err error
	if isPinger {
		sock, err = req.NewSocket()
	} else {
		sock, err = rep.NewSocket()
	}
	if err != nil {
		panic(err)
	}

	if isPinger {
		err := sock.Dial(fmt.Sprintf("tcp://%s:%d", endpoint, mangosPort))
		if err != nil {
			panic(err)
		}
		go func() {
			for {
				start := time.Now()
				sock.Send([]byte("ping?"))
				msg, err := sock.Recv()
				if err != nil {
					panic(err)
				}
				strMsg := string(msg)
				if strMsg != "pong!" {
					panic(fmt.Sprintf("non-pong message '%s'", strMsg))
				}
				elapsed := time.Now().Sub(start)
				printFLn("mangos    round-trip took %dns (%dms)", elapsed.Nanoseconds(), elapsed.Milliseconds())
				time.Sleep(time.Second)
			}
		}()
	} else {
		sock.Listen(fmt.Sprintf("tcp://:%d", mangosPort))
		printFLn("Listening to mangos on port %d", mangosPort)
		go func() {
			for {
				msg, err := sock.Recv()
				if err != nil && !errors.Is(err, mangos.ErrClosed) {
					panic(err)
				}
				strMsg := string(msg)
				if strMsg != "ping?" {
					panic(fmt.Sprintf("non-ping message '%s'", strMsg))
				}
				sock.Send([]byte("pong!"))
				println("ponged")
			}
		}()
	}

	<-signals
	sock.Close()
}

func startZmq(isPinger bool, endpoint string, ewg sync.WaitGroup) {
	ewg.Add(1)
	defer ewg.Done()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	ctx, err := zmq.NewContext()
	ctx.SetIpv6(true)

	shutdownPublisher, _ := ctx.NewSocket(zmq.PUB)
	shutdownPublisher.SetLinger(0)
	shutdownCh := "inproc://shutdown"
	shutdownPublisher.Bind(shutdownCh)
	if err != nil {
		panic(err)
	}
	var wg sync.WaitGroup
	go func() {
		wg.Add(1)
		defer wg.Done()

		shutdownListener, _ := ctx.NewSocket(zmq.SUB)
		shutdownListener.SetLinger(0)
		shutdownListener.SetSubscribe("")
		shutdownListener.Connect(shutdownCh)

		var socket *zmq.Socket
		if isPinger {
			socket, _ = ctx.NewSocket(zmq.REQ)
		} else {
			socket, _ = ctx.NewSocket(zmq.REP)
		}
		socket.SetLinger(0)
		if isPinger {
			socket.Connect(fmt.Sprintf("tcp://%s:%d", endpoint, zmqPort))
		} else {
			socket.Bind(fmt.Sprintf("tcp://*:%d", zmqPort))
			printFLn("Listening to zmq on port %d", zmqPort)
		}

		poller := zmq.NewPoller()
		poller.Add(shutdownListener, zmq.POLLIN)
		poller.Add(socket, zmq.POLLIN)

		if isPinger {
			defer func() {
				shutdownListener.Close()
				socket.Close()
			}()
			for {
				start := time.Now()
				socket.Send("ping?", 0)

				sockets, _ := poller.Poll(-1)
				for _, soc := range sockets {
					switch s := soc.Socket; s {
					case socket:
						msg, _ := s.Recv(0)
						if msg != "pong!" {
							panic(fmt.Sprintf("non-pong message '%s'", msg))
						}
						elapsed := time.Now().Sub(start)
						printFLn("zmq       round-trip took %dns (%dms)", elapsed.Nanoseconds(), elapsed.Milliseconds())
					case shutdownListener:
						s.Recv(0)
						shutdownListener.Close()
						socket.Close()
						return
					}
				}
				time.Sleep(1 * time.Second)
			}
		} else {
			for {
				sockets, _ := poller.Poll(-1)
				for _, soc := range sockets {
					switch s := soc.Socket; s {
					case socket:
						msg, _ := s.Recv(0)
						if msg != "ping?" {
							panic(fmt.Sprintf("non-ping message '%s'", msg))
						}
						s.Send("pong!", 0)
						println("ponged")
					case shutdownListener:
						s.Recv(0)
						shutdownListener.Close()
						socket.Close()
						return
					}
				}
			}
		}

	}()

	<-signals
	shutdownPublisher.Send("", 0)
	shutdownPublisher.Close()
	wg.Wait()
	ctx.Term()
}
