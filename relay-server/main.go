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
	"time"
)

var currConn net.Conn
var currHttpServer *http.Server

func handleCamConnect(conn net.Conn, recordDir, mjpegAddr, mjpegPath, flvAddr, rtmpAddr string) {
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

	log.Printf("mjpeg address: %s%s\n", mjpegAddr, mjpegPath)

	wg := sync.WaitGroup{}
	wg.Add(1)

	streamConsumers := make([]chan []byte, 0)

	defer func() {
		for _, consumer := range streamConsumers {
			close(consumer)
		}
	}()

	removeStreamConsumer := func(consumer chan []byte) {
		target := -1
		for i, ele := range streamConsumers {
			if consumer == ele {
				target = i
				return
			}
		}
		streamConsumers[target] = streamConsumers[len(streamConsumers)-1]
		streamConsumers = streamConsumers[:len(streamConsumers)-1]
	}

	go func() {
		defer wg.Done()

		multipartReader := multipart.NewReader(conn, "123456789000000000000987654321")
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
	mux.HandleFunc(mjpegPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Content-Type", "multipart/x-mixed-replace;boundary=123456789000000000000987654321")
		w.Header().Set("Connection", "Keep-Alive")
		w.Header().Set("X-Content-Type-Options", "nosniff")

		receiverChan := make(chan []byte)
		streamConsumers = append(streamConsumers, receiverChan)

		defer removeStreamConsumer(receiverChan)

		multipartWriter := multipart.NewWriter(w)
		multipartWriter.SetBoundary("123456789000000000000987654321")

		for {
			select {
			case imageBytes := <-receiverChan:
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
			case <-time.After(time.Second * 10):
				return
			}
		}
	})

	srv := &http.Server{Addr: mjpegAddr, Handler: mux}
	go srv.ListenAndServe()
	defer func() {
		srv.Shutdown(context.TODO())
		currHttpServer = nil
	}()

	currHttpServer = srv

	if recordDir != "" {
		os.MkdirAll(recordDir, 0755)
		cmdFfmpeg := exec.Command("./ffmpeg", "-use_wallclock_as_timestamps", "1", "-i", fmt.Sprintf("http://127.0.0.1%s%s", mjpegAddr, mjpegPath), "-f", "lavfi", "-i", "anullsrc", "-c:v", "libx264", "-vf", "format=yuv420p", "-crf", "30", "-maxrate", "800k", "-g", "30", "-fflags", "nobuffer", "-c:a", "aac", "-b:a", "1k", path.Join(recordDir, "record.mp4"))
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
	recordDir := flag.String("record-dir", "", "dir to put recording files")
	camAddr := flag.String("cam-addr", ":40001", "camera connect address")
	mjpegAddr := flag.String("mjpeg-addr", ":40002", "mjpeg address")
	mjpegPath := flag.String("mjpeg-path", "/cam", "mjpeg path")
	flvAddr := flag.String("flv-addr", ":40003", "transcoded flv stream address")
	rtmpAddr := flag.String("rtmp-addr", "127.0.0.1:40004", "rtmp address for internal usage")
	flag.Parse()

	listener, err := net.Listen("tcp", *camAddr)
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()

	log.Println("waiting for camera connection on", *camAddr)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Fatal(err)
		}
		log.Println("new camera connection", conn.RemoteAddr().String())

		go handleCamConnect(conn, *recordDir, *mjpegAddr, *mjpegPath, *flvAddr, *rtmpAddr)
	}
}
