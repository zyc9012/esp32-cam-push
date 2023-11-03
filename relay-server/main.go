package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"os"
	"os/exec"
	"path"
	"sync"
	"time"
)

const multipartBoundary = "123456789000000000000987654321"

var currConn net.Conn
var currHttpServer *http.Server

type Config struct {
	CamAddr        string
	MjpegAddr      string
	MjpegPath      string
	MjpegFrameRate string
	RecordDir      string
	RecordSegment  string
	FfmpegPath     string
	FontPath       string
}

func handleCamConnect(conn net.Conn, config Config) {
	// Verify conn
	header := make([]byte, 32)
	conn.SetReadDeadline(time.Now().Add(time.Second * 10))
	_, err := conn.Read(header)
	if err != nil || !bytes.Equal(header, []byte{0xa6, 0xf6, 0xa0, 0x7b, 0xe9, 0xb6, 0xd0, 0xe5, 0x73, 0x4e, 0x06, 0x59, 0xcf, 0xc7, 0xa3, 0xe9, 0xda, 0xca, 0xb5, 0x82, 0xf9, 0x11, 0xfe, 0xc7, 0x7f, 0xc0, 0xc4, 0x16, 0x57, 0x7d, 0xea, 0x06}) {
		conn.Close()
		log.Println("illegal connection", conn.RemoteAddr().String())
		return
	}
	conn.SetReadDeadline(time.Time{})

	log.Println("new camera connection", conn.RemoteAddr().String())

	defer func() {
		conn.Close()
		currConn = nil
	}()

	if currConn != nil {
		currConn.Close()
		currConn = nil
	}

	currConn = conn

	if currHttpServer != nil {
		currHttpServer.Shutdown(context.TODO())
		currHttpServer = nil
	}

	log.Printf("mjpeg address: %s%s\n", config.MjpegAddr, config.MjpegPath)

	wg := sync.WaitGroup{}
	wg.Add(1)

	var streamConsumers []chan []byte

	removeStreamConsumer := func(consumer chan []byte) {
		target := -1
		for i, ele := range streamConsumers {
			if consumer == ele {
				target = i
				return
			}
		}
		if target >= 0 {
			streamConsumers[target] = streamConsumers[len(streamConsumers)-1]
			streamConsumers = streamConsumers[:len(streamConsumers)-1]
		}
	}

	go func() {
		defer wg.Done()

		multipartReader := multipart.NewReader(conn, multipartBoundary)
		for {
			nextPart, err := multipartReader.NextRawPart()
			if err != nil {
				return
			}

			partData, err := io.ReadAll(nextPart)
			if err != nil {
				return
			}

			for _, consumer := range streamConsumers {
				select {
				case consumer <- partData:
				default:
				}
			}
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc(config.MjpegPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Content-Type", "multipart/x-mixed-replace;boundary="+multipartBoundary)
		w.Header().Set("Connection", "Keep-Alive")
		w.Header().Set("X-Content-Type-Options", "nosniff")

		receiverChan := make(chan []byte)
		streamConsumers = append(streamConsumers, receiverChan)

		defer removeStreamConsumer(receiverChan)

		multipartWriter := multipart.NewWriter(w)
		multipartWriter.SetBoundary(multipartBoundary)

		for {
			imageBytes, ok := <-receiverChan
			if !ok {
				return
			}
			header := textproto.MIMEHeader{}
			header.Add("Content-Type", "image/jpeg")
			part, err := multipartWriter.CreatePart(header)
			if err != nil {
				return
			}
			_, err = part.Write(imageBytes)
			if err != nil {
				return
			}
		}
	})
	mux.Handle(config.MjpegPath+"/records/", http.StripPrefix(config.MjpegPath+"/records/", http.FileServer(http.Dir(config.RecordDir))))

	srv := &http.Server{Addr: config.MjpegAddr, Handler: mux}
	go srv.ListenAndServe()
	defer func() {
		for _, consumer := range streamConsumers {
			close(consumer)
		}
		streamConsumers = nil

		srv.Shutdown(context.TODO())
		currHttpServer = nil
	}()

	currHttpServer = srv

	if config.RecordDir != "" {
		os.MkdirAll(config.RecordDir, 0755)
		cmdFfmpeg := exec.Command(config.FfmpegPath, "-r", config.MjpegFrameRate, "-i", fmt.Sprintf("http://127.0.0.1%s%s", config.MjpegAddr, config.MjpegPath),
			"-f", "lavfi", "-i", "anullsrc",
			"-c:v", "libx264", "-vf", "format=yuv420p, drawtext=text='%{localtime\\:%Y/%m/%d %H\\\\\\:%M\\\\\\:%S}':x=0:y=0:fontsize=24:fontcolor=white:fontfile='"+config.FontPath+"'", "-crf", "30",
			"-c:a", "aac", "-b:a", "1k",
			"-f", "segment", "-segment_time", config.RecordSegment, "-strftime", "1",
			path.Join(config.RecordDir, "%Y-%m-%d_%H-%M.flv"))
		if err := cmdFfmpeg.Start(); err == nil {
			defer cmdFfmpeg.Process.Signal(os.Interrupt)
			go cmdFfmpeg.Wait()
		} else {
			fmt.Println(err)
		}
	}

	wg.Wait()

	log.Println("closing", conn.RemoteAddr().String())
}

func main() {
	var config Config
	flag.StringVar(&config.CamAddr, "cam-addr", ":40001", "camera connect address")
	flag.StringVar(&config.MjpegAddr, "mjpeg-addr", ":40002", "mjpeg address")
	flag.StringVar(&config.MjpegPath, "mjpeg-path", "/cam", "mjpeg path")
	flag.StringVar(&config.MjpegFrameRate, "mjpeg-fr", "15", "mjpeg frame rate")
	flag.StringVar(&config.RecordDir, "record-dir", "", "dir to put recording files")
	flag.StringVar(&config.RecordSegment, "record-seg", "3600", "segment duration of recording files")
	flag.StringVar(&config.FfmpegPath, "ffmpeg-path", "ffmpeg", "path to the ffmpeg executable")
	flag.StringVar(&config.FontPath, "font-path", "Monaco.ttf", "path to the font file used by ffmpeg to render timestamp")
	flag.Parse()

	listener, err := net.Listen("tcp", config.CamAddr)
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()

	log.Println("waiting for camera connection on", config.CamAddr)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Fatal(err)
		}

		go handleCamConnect(conn, config)
	}
}
