package main

import (
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
)

const multipartBoundary = "123456789000000000000987654321"

var currConn net.Conn
var currHttpServer *http.Server

type Config struct {
	CamAddr       string
	MjpegAddr     string
	MjpegPath     string
	RecordDir     string
	RecordSegment string
	FfmpegPath    string
	FontPath      string
}

func handleCamConnect(conn net.Conn, config Config) {
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
		cmdFfmpeg := exec.Command(config.FfmpegPath, "-use_wallclock_as_timestamps", "1", "-i", fmt.Sprintf("http://127.0.0.1%s%s", config.MjpegAddr, config.MjpegPath),
			"-f", "lavfi", "-i", "anullsrc",
			"-c:v", "libx264", "-vf", "format=yuv420p, drawtext=text='%{localtime\\:%Y/%m/%d %H\\\\\\:%M\\\\\\:%S}':x=0:y=0:fontsize=24:fontcolor=white:fontfile='"+config.FontPath+"'", "-crf", "30", "-maxrate", "800k", "-r", "15",
			"-c:a", "aac", "-b:a", "1k",
			"-f", "segment", "-segment_time", config.RecordSegment, "-strftime", "1",
			path.Join(config.RecordDir, "%Y-%m-%d_%H-%M.mp4"))
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
		log.Println("new camera connection", conn.RemoteAddr().String())

		go handleCamConnect(conn, config)
	}
}
