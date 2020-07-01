package httpflv

import (
	"encoding/json"
	log "github.com/sirupsen/logrus"
	"github.com/vcarecity/go-stream-live/media/av"
	"github.com/vcarecity/go-stream-live/media/protocol/amf"
	"github.com/vcarecity/go-stream-live/media/protocol/rtmp"
	"github.com/vcarecity/go-stream-live/media/utils/pio"
	"github.com/vcarecity/go-stream-live/media/utils/uid"
	"net"
	"net/http"
	neturl "net/url"
	"strings"
	"time"

	"errors"
)

type FLVConnectCallback func(url *neturl.URL, headers http.Header)
type FLVCloseCallback func(url string, app string, uid string)

type Server struct {
	handler       av.Handler
	connCallback  FLVConnectCallback
	closeCallback FLVCloseCallback
}

type stream struct {
	Key string `json:"key"`
	Id  string `json:"id"`
}

type streams struct {
	Publishers []stream `json:"publishers"`
	Players    []stream `json:"players"`
}

func NewServer(h av.Handler) *Server {
	return &Server{
		handler: h,
	}
}
func NewServerFunc(h av.Handler, connCallback FLVConnectCallback, callback FLVCloseCallback) *Server {
	return &Server{
		handler:       h,
		connCallback:  connCallback,
		closeCallback: callback,
	}
}

func (self *Server) Serve(l net.Listener) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		self.handleConn(w, r)
	})
	mux.HandleFunc("/streams", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		self.getStream(w, r)
	})
	http.Serve(l, mux)
	return nil
}

func (s *Server) getStream(w http.ResponseWriter, r *http.Request) {
	rtmpStream := s.handler.(*rtmp.RtmpStream)
	if rtmpStream == nil {
		return
	}
	msgs := new(streams)
	for item := range rtmpStream.GetStreams().IterBuffered() {
		if s, ok := item.Val.(*rtmp.Stream); ok {
			if s.GetReader() != nil {
				msg := stream{item.Key, s.GetReader().Info().UID}
				msgs.Publishers = append(msgs.Publishers, msg)
			}
		}
	}

	for item := range rtmpStream.GetStreams().IterBuffered() {
		ws := item.Val.(*rtmp.Stream).GetWs()
		for s := range ws.IterBuffered() {
			if pw, ok := s.Val.(*rtmp.PackWriterCloser); ok {
				if pw.GetWriter() != nil {
					msg := stream{item.Key, pw.GetWriter().Info().UID}
					msgs.Players = append(msgs.Players, msg)
				}
			}
		}
	}
	resp, _ := json.Marshal(msgs)
	w.Header().Set("Content-Type", "application/json")
	w.Write(resp)

}

func (self *Server) handleConn(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorln("http flv handleConn panic: ", r)
		}
	}()

	if self.connCallback != nil {
		go self.connCallback(r.URL, r.Header)
	}

	url := r.URL.String()
	u := r.URL.Path
	if pos := strings.LastIndex(u, "."); pos < 0 || u[pos:] != ".flv" {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	path := strings.TrimSuffix(strings.TrimLeft(u, "/"), ".flv")
	paths := strings.SplitN(path, "/", 2)

	log.Debugf("url: %s. path: %s. paths: %s.", u, path, paths)

	if len(paths) != 2 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	writer := NewFLVWriter(paths[0], paths[1], url, w, self.closeCallback)

	self.handler.HandleWriter(writer)
	writer.Wait()
}

const (
	headerLen   = 11
	maxQueueNum = 1024
)

type FLVWriter struct {
	Uid string
	av.RWBaser
	app, title, url string
	buf             []byte
	closed          bool
	closedChan      chan struct{}
	ctx             http.ResponseWriter
	packetQueue     chan av.Packet
	closeCallback   FLVCloseCallback
}

func NewFLVWriter(app, title, url string, ctx http.ResponseWriter, callback FLVCloseCallback) *FLVWriter {
	ret := &FLVWriter{
		Uid:           uid.NEWID(),
		app:           app,
		title:         title,
		url:           url,
		ctx:           ctx,
		RWBaser:       av.NewRWBaser(time.Second * 10),
		closedChan:    make(chan struct{}),
		buf:           make([]byte, headerLen),
		packetQueue:   make(chan av.Packet, maxQueueNum),
		closeCallback: callback,
	}

	ret.ctx.Write([]byte{0x46, 0x4c, 0x56, 0x01, 0x05, 0x00, 0x00, 0x00, 0x09})
	pio.PutI32BE(ret.buf[:4], 0)
	ret.ctx.Write(ret.buf[:4])
	go func() {
		err := ret.SendPacket()
		if err != nil {
			log.Errorf("SendPacket error: %v", err)
			ret.closed = true
		}
		// send stream to http flv stop ..
	}()
	return ret
}

func (self *FLVWriter) DropPacket(pktQue chan av.Packet, info av.Info) {
	log.Errorf("[%v] packet queue max!!!", info)
	for i := 0; i < maxQueueNum-84; i++ {
		tmpPkt, ok := <-pktQue
		if ok {
			// try to don't drop audio
			if tmpPkt.IsAudio {
				log.Infoln("insert audio to queue")
				if len(pktQue) > maxQueueNum-2 {
					<-pktQue
				} else {
					pktQue <- tmpPkt
				}
			}

			if tmpPkt.IsVideo {
				videoPkt, _ := tmpPkt.Header.(av.VideoPacketHeader)
				if len(pktQue) > maxQueueNum-10 {
					<-pktQue
				}
				// dont't drop sps config and dont't drop key frame
				if videoPkt.IsSeq() || videoPkt.IsKeyFrame() {
					log.Infoln("insert keyframe to queue")
					pktQue <- tmpPkt
				}
			}

		}

	}
	log.Infoln("packet queue len: ", len(pktQue))
}

func (self *FLVWriter) Write(p av.Packet) error {
	// stream write packet to here
	if !self.closed {
		// add to queue or drop
		if len(self.packetQueue) >= maxQueueNum-24 {
			self.DropPacket(self.packetQueue, self.Info())
		} else {
			self.packetQueue <- p
		}
		return nil
	} else {
		if self != nil {
			// update callback
			go self.closeCallback(self.url, self.app, self.Uid)
		}
		return errors.New("closed")
	}

}

// func (self *FLVWriter) Write(p av.Packet) error {
func (self *FLVWriter) SendPacket() error {
	for {
		p, ok := <-self.packetQueue
		if ok {
			self.RWBaser.SetPreTime()
			h := self.buf[:headerLen]
			typeID := av.TAG_VIDEO
			if !p.IsVideo {
				if p.IsMetadata {
					var err error
					typeID = av.TAG_SCRIPTDATAAMF0
					p.Data, err = amf.MetaDataReform(p.Data, amf.DEL)
					if err != nil {
						return err
					}
				} else {
					typeID = av.TAG_AUDIO
				}
			}
			dataLen := len(p.Data)
			timestamp := p.TimeStamp
			timestamp += self.BaseTimeStamp()
			self.RWBaser.RecTimeStamp(timestamp, uint32(typeID))

			preDataLen := dataLen + headerLen
			timestampbase := timestamp & 0xffffff
			timestampExt := timestamp >> 24 & 0xff

			pio.PutU8(h[0:1], uint8(typeID))
			pio.PutI24BE(h[1:4], int32(dataLen))
			pio.PutI24BE(h[4:7], int32(timestampbase))
			pio.PutU8(h[7:8], uint8(timestampExt))

			if _, err := self.ctx.Write(h); err != nil {
				return err
			}

			if _, err := self.ctx.Write(p.Data); err != nil {
				return err
			}

			pio.PutI32BE(h[:4], int32(preDataLen))
			if _, err := self.ctx.Write(h[:4]); err != nil {
				return err
			}
		} else {
			return errors.New("closed")
		}
	}

	return nil
}

func (self *FLVWriter) Wait() {
	select {
	case <-self.closedChan:
		return
	}
}

func (self *FLVWriter) Close(error) {
	log.Infoln("http flv closed")
	if !self.closed {
		close(self.packetQueue)
		close(self.closedChan)
	}
	self.closed = true
}

func (self *FLVWriter) Info() (ret av.Info) {
	ret.UID = self.Uid
	ret.Type = "http-flv"
	ret.URL = self.url
	ret.Key = self.app + "/" + self.title
	ret.Inter = true
	return
}
