// Copyright 2015-2016 Espressif Systems (Shanghai) PTE LTD
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
#include <WiFi.h>
#include "esp_timer.h"
#include "esp_camera.h"
#include "img_converters.h"
#include "fb_gfx.h"
#include "esp32-hal-ledc.h"
#include "sdkconfig.h"

#if defined(ARDUINO_ARCH_ESP32) && defined(CONFIG_ARDUHAL_ESP_LOG)
#include "esp32-hal-log.h"
#endif

#define LED_LEDC_CHANNEL 2 //Using different ledc channel/timer than camera

#define PART_BOUNDARY "123456789000000000000987654321"
static const char _STREAM_HANDSHAKE[] = { 0xa6, 0xf6, 0xa0, 0x7b, 0xe9, 0xb6, 0xd0, 0xe5, 0x73, 0x4e, 0x06, 0x59, 0xcf, 0xc7, 0xa3, 0xe9, 0xda, 0xca, 0xb5, 0x82, 0xf9, 0x11, 0xfe, 0xc7, 0x7f, 0xc0, 0xc4, 0x16, 0x57, 0x7d, 0xea, 0x06 };
static const char *_STREAM_CONTENT_TYPE = "multipart/x-mixed-replace;boundary=" PART_BOUNDARY;
static const char *_STREAM_BOUNDARY = "\r\n--" PART_BOUNDARY "\r\n";
static const char *_STREAM_PART = "Content-Type: image/jpeg\r\nContent-Length: %u\r\nX-Timestamp: %d.%06d\r\n\r\n";

static void set_cam_conf() {
  sensor_t *s = esp_camera_sensor_get();

  if (s->pixformat == PIXFORMAT_JPEG) {
    s->set_framesize(s, FRAMESIZE_XGA);
  }
  s->set_quality(s, 10);
  s->set_contrast(s, 0);
  s->set_brightness(s, 0);
  s->set_saturation(s, 0);
  s->set_gainceiling(s, GAINCEILING_2X);
  s->set_colorbar(s, 0);
  s->set_whitebal(s, 1);
  s->set_gain_ctrl(s, 1);
  s->set_exposure_ctrl(s, 1);
  s->set_hmirror(s, 0);
  s->set_vflip(s, 0);
  s->set_awb_gain(s, 1);
  s->set_agc_gain(s, 0);
  s->set_dcw(s, 1);
  s->set_bpc(s, 0);
  s->set_wpc(s, 1);
  s->set_raw_gma(s, 1);
  s->set_lenc(s, 1);
  s->set_special_effect(s, 0);
  s->set_wb_mode(s, 0);
  s->set_ae_level(s, 0);
}

static esp_err_t send_cam_stream(WiFiClient &client) {
  camera_fb_t *fb = NULL;
  struct timeval _timestamp;
  esp_err_t res = ESP_OK;
  size_t _jpg_buf_len = 0;
  uint8_t *_jpg_buf = NULL;
  char *part_buf[128];

  static int64_t last_frame = 0;
  if (!last_frame) {
    last_frame = esp_timer_get_time();
  }

  while (true) {
    res = ESP_OK;

    fb = esp_camera_fb_get();
    if (!fb) {
      log_e("Camera capture failed");
      res = ESP_FAIL;
    } else {
      _timestamp.tv_sec = fb->timestamp.tv_sec;
      _timestamp.tv_usec = fb->timestamp.tv_usec;
      if (fb->format != PIXFORMAT_JPEG) {
        bool jpeg_converted = frame2jpg(fb, 80, &_jpg_buf, &_jpg_buf_len);
        esp_camera_fb_return(fb);
        fb = NULL;
        if (!jpeg_converted) {
          log_e("JPEG compression failed");
          res = ESP_FAIL;
        }
      } else {
        _jpg_buf_len = fb->len;
        _jpg_buf = fb->buf;
      }
    }

    if (res == ESP_FAIL) {
      continue;
    }

    size_t written = client.write(_STREAM_BOUNDARY, strlen(_STREAM_BOUNDARY));

    if (written > 0) {
      size_t hlen = snprintf((char *)part_buf, 128, _STREAM_PART, _jpg_buf_len, _timestamp.tv_sec, _timestamp.tv_usec);
      written = client.write((const uint8_t *)part_buf, hlen);
    }
    if (written > 0) {
      written = client.write((const uint8_t *)_jpg_buf, _jpg_buf_len);
    }
    if (fb) {
      esp_camera_fb_return(fb);
      fb = NULL;
      _jpg_buf = NULL;
    } else if (_jpg_buf) {
      free(_jpg_buf);
      _jpg_buf = NULL;
    }
    if (written <= 0) {
      log_e("Send frame failed");
      res = ESP_FAIL;
      break;
    }
    int64_t fr_end = esp_timer_get_time();
    int64_t frame_time = fr_end - last_frame;
    frame_time /= 1000;
    last_frame = fr_end;

    log_i("MJPG: %uB %ums (%.1ffps)",
          (uint32_t)(_jpg_buf_len),
          (uint32_t)frame_time, 1000.0 / (uint32_t)frame_time);
  }

  return res;
}

void sendCameraFrames(const IPAddress& server, uint16_t port) {
  WiFiClient client;
  client.setTimeout(5);

  log_i("Connecting %s:%d\n", server.toString().c_str(), port);
  if (client.connect(server, port)) {
    client.setTimeout(0);
    log_i("Sending camera stream");
    set_cam_conf();
    client.write(_STREAM_HANDSHAKE, sizeof(_STREAM_HANDSHAKE));
    send_cam_stream(client);
    client.stop();
  }
  delay(1000);
}

void setupLedFlash(int pin) {
  ledcSetup(LED_LEDC_CHANNEL, 5000, 8);
  ledcAttachPin(pin, LED_LEDC_CHANNEL);
}
