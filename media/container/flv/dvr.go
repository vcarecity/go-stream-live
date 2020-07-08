package flv

import (
	"github.com/vcarecity/go-stream-live/log"
	"github.com/vcarecity/go-stream-live/media/av"
	"github.com/vcarecity/go-stream-live/media/protocol/amf"
	"github.com/vcarecity/go-stream-live/media/utils/pio"
	"github.com/vcarecity/go-stream-live/media/utils/uid"
	"path/filepath"
	"strings"
	"time"

	"os"

	"fmt"
)

var (
	flvHeader = []byte{0x46, 0x4c, 0x56, 0x01, 0x05, 0x00, 0x00, 0x00, 0x09}
)

type FlvDvr struct {
	Dir string
}

func NewFlvDvr(dir string) *FlvDvr {
	return &FlvDvr{Dir: dir}
}

func (f *FlvDvr) GetWriter(info av.Info) av.WriteCloser {
	paths := strings.SplitN(info.Key, "/", 2)
	if len(paths) != 2 {
		log.Logger().Errorln("invalid info")
		return nil
	}

	saveDir := filepath.Join(f.Dir, paths[0])
	_, err := os.Stat(saveDir)
	if err != nil {
		err := os.MkdirAll(saveDir, 0755)
		if err != nil {
			log.Logger().Errorln("mkdir error:", err)
			return nil
		}
	}

	fileName := fmt.Sprintf("%s_%d.%s", filepath.Join(f.Dir, info.Key), time.Now().Unix(), "flv")

	log.Logger().Debugf("flv dvr save stream to: %s", fileName)

	w, err := os.OpenFile(fileName, os.O_CREATE|os.O_RDWR, 0755)
	if err != nil {
		log.Logger().Errorln("open file error: ", err)
		return nil
	}

	writer := NewFLVWriter(paths[0], paths[1], info.URL, w)
	log.Logger().Infoln("new flv dvr: ", writer.Info())
	return writer
}

const (
	headerLen = 11
)

type FLVWriter struct {
	Uid string
	av.RWBaser
	app, title, url string
	buf             []byte
	closed          bool
	ctx             *os.File
}

func NewFLVWriter(app, title, url string, ctx *os.File) *FLVWriter {
	ret := &FLVWriter{
		Uid:     uid.NEWID(),
		app:     app,
		title:   title,
		url:     url,
		ctx:     ctx,
		RWBaser: av.NewRWBaser(time.Second * 10),
		buf:     make([]byte, headerLen),
	}

	ret.ctx.Write(flvHeader)
	pio.PutI32BE(ret.buf[:4], 0)
	ret.ctx.Write(ret.buf[:4])

	return ret
}

func (self *FLVWriter) Write(p av.Packet) error {
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

	return nil
}

func (self *FLVWriter) Close(error) {
	log.Logger().Infoln("flv dvr closed")
	self.ctx.Close()
}

func (self *FLVWriter) Info() (ret av.Info) {
	ret.UID = self.Uid
	ret.Type = "flv-dvr"
	ret.URL = self.url
	ret.Key = self.app + "/" + self.title
	return
}
