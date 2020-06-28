package main

import (
	"flag"
	"fmt"
	nested "github.com/antonfisher/nested-logrus-formatter"
	log "github.com/sirupsen/logrus"
	"go-stream-live/media/av"
	"go-stream-live/media/container/flv"
	"go-stream-live/media/protocol/hls"
	"go-stream-live/media/protocol/httpflv"
	"go-stream-live/media/protocol/httpopera"
	"go-stream-live/media/protocol/rtmp"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"syscall"

	"time"
)

const (
	programName = "SMS"
	VERSION     = "1.1.1"
)

var (
	buildTime string
	prof      = flag.String("pprofAddr", "", "golang pprof debug address.")
	rtmpAddr  = flag.String("rtmpAddr", ":1935", "The rtmp server address to bind.")
	flvAddr   = flag.String("flvAddr", ":8081", "the http-flv server address to bind.")
	hlsAddr   = flag.String("hlsAddr", ":8080", "the hls server address to bind.")
	operaAddr = flag.String("operaAddr", "", "the http operation or config address to bind: 8082.")
	flvDvr    = flag.Bool("flvDvr", true, "enable flv dvr")
)

var (
	Getters []av.GetWriter
)

func BuildTime() string {
	return buildTime
}

func init() {
	log.SetFormatter(&nested.Formatter{
		HideKeys:        true,
		TimestampFormat: "2006-01-02 15:04:05",
		FieldsOrder:     []string{"component", "category"},
	})
	log.SetLevel(log.DebugLevel)
	log.SetOutput(os.Stdout)

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "%s Version[%s]\r\nUsage: %s [OPTIONS]\r\n", programName, VERSION, os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()
}

func catchSignal() {
	// windows unsupport syscall.SIGSTOP
	sig := make(chan os.Signal)
	signal.Notify(sig, syscall.SIGTERM)
	<-sig
	log.Fatalln("recieved signal!")
	// select {}
}

func main() {

	defer func() {
		if r := recover(); r != nil {
			log.Errorln("main panic: ", r)
			time.Sleep(1 * time.Second)
		}
	}()

	stream := rtmp.NewRtmpStream()
	// hls
	startHls()
	// flv dvr
	startFlvDvr()
	// rtmp
	startRtmp(stream, Getters)
	// http-flv
	startHTTPFlv(stream)
	// http-opera
	startHTTPOpera(stream)
	// pprof
	startPprof()
	// my log
	mylog()
	// block
	catchSignal()
}

func startHls() *hls.Server {
	hlsListen, err := net.Listen("tcp", *hlsAddr)
	if err != nil {
		log.Fatal(err)
	}

	hlsServer := hls.NewServer()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Errorln("hls server panic: ", r)
			}
		}()
		hlsServer.Serve(hlsListen)
	}()
	Getters = append(Getters, hlsServer)
	return hlsServer
}

func startRtmp(stream *rtmp.RtmpStream, getters []av.GetWriter) {
	rtmplisten, err := net.Listen("tcp", *rtmpAddr)
	if err != nil {
		log.Fatal(err)
	}

	rtmpServer := rtmp.NewRtmpServer(stream, getters)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Errorln("hls server panic: ", r)
			}
		}()
		rtmpServer.Serve(rtmplisten)
	}()
}

func startHTTPFlv(stream *rtmp.RtmpStream) {
	flvListen, err := net.Listen("tcp", *flvAddr)
	if err != nil {
		log.Fatal(err)
	}

	hdlServer := httpflv.NewServer(stream)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Errorln("hls server panic: ", r)
			}
		}()
		hdlServer.Serve(flvListen)
	}()
}

func startHTTPOpera(stream *rtmp.RtmpStream) {
	if *operaAddr != "" {
		opListen, err := net.Listen("tcp", *operaAddr)
		if err != nil {
			log.Fatal(err)
		}
		opServer := httpopera.NewServer(stream)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Errorln("hls server panic: ", r)
				}
			}()
			opServer.Serve(opListen)
		}()
	}
}

func startFlvDvr() {
	if *flvDvr {
		// fd := new(flv.FlvDvr)
		fd := flv.NewFlvDvr(`E:\GoPath\src\go-stream-live\build`)
		Getters = append(Getters, fd)
	}
}

func startPprof() {
	if *prof != "" {
		go func() {
			if err := http.ListenAndServe(*prof, nil); err != nil {
				log.Fatal("enable pprof failed: ", err)
			}
		}()
	}
}

func mylog() {
	log.Printf("SMS Version:  %s\tBuildTime:  %s", VERSION, BuildTime())
	log.Printf("SMS Start, Rtmp Listen On %s", *rtmpAddr)
	log.Printf("SMS Start, Hls Listen On %s", *hlsAddr)
	log.Printf("SMS Start, HTTP-flv Listen On %s", *flvAddr)
	if *operaAddr != "" {
		log.Printf("SMS Start, HTTP-Operation Listen On %s", *operaAddr)
	}
	if *prof != "" {
		log.Printf("SMS Start, Pprof Server Listen On %s", *prof)
	}
	if *flvDvr {
		log.Printf("SMS Start, Flv Dvr Save On [%s]", "app/streamName.flv")
	}
	// SavePid()
}
