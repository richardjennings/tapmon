package cmd

import (
	"context"
	"fmt"
	"github.com/golang/protobuf/proto"
	"github.com/golang/snappy"
	"github.com/prometheus/common/config"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/prompb"
	"github.com/prometheus/prometheus/storage/remote"
	"github.com/richardjennings/tapo/pkg/tapo"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"net/url"
	"os"
	"sync"
	"time"
)

type (
	Config struct {
		Interval   int
		Devices    []Device
		Prometheus struct {
			Endpoint      string
			Username      string
			Password      string
			FlushInterval int
		}
	}
	Device struct {
		Ip       string
		Username string
		Password string
	}
	client struct {
		t *tapo.Tapo
		d Device
	}
)

func init() {
	var l log.Level
	var err error
	lvl := os.Getenv("TAPMON_LOGLEVEL")
	l, err = log.ParseLevel(lvl)
	if err != nil {
		l = log.WarnLevel
		if lvl != "" {
			log.Warningf("could not use log level %s, using default level %s", lvl, l)
		}
	}
	log.SetLevel(l)
}

var daemonCmd = &cobra.Command{
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var cs []client
		var conf Config
		var t *tapo.Tapo
		var err error

		viper.SetConfigFile(args[0])
		viper.SetDefault("Interval", 5*60)
		viper.SetDefault("Prometheus.FlushInterval", 5*60)
		cobra.CheckErr(viper.ReadInConfig())
		cobra.CheckErr(viper.Unmarshal(&conf))

		if len(conf.Devices) == 0 {
			cobra.CheckErr("no Devices configured")
		}

		for _, d := range conf.Devices {
			// check we can communicate with Device
			t, err = tapo.NewTapo(d.Ip, d.Username, d.Password)
			if err != nil {
				cobra.CheckErr(fmt.Sprintf("could not connect to Device with ip %s", d.Ip))
			}
			cs = append(cs, client{t: t, d: d})
			log.Infof("connected to device %s", d.Ip)
		}

		stop := make(chan bool)
		metrics := make(chan prompb.TimeSeries)

		wg := sync.WaitGroup{}

		for _, c := range cs {
			wg.Add(1)
			log.Infof("starting CollectEnergyUsage for %s", c.d.Ip)
			go CollectEnergyUsage(&wg, stop, conf.Interval, c, metrics)
		}
		log.Info("starting RemoteWriter")
		wg.Add(1)
		go RemoteWrite(&wg, stop, metrics, conf)

		wg.Wait()

		return nil
	},
}

func Execute() {
	cobra.CheckErr(daemonCmd.Execute())
}

func RemoteWrite(wg *sync.WaitGroup, stop chan bool, metrics chan prompb.TimeSeries, conf Config) {
	var ok bool
	var ts prompb.TimeSeries
	var err error
	var data []byte
	var writeReq *prompb.WriteRequest
	var c remote.WriteClient
	var endpoint *url.URL
	var encoded []byte

	if endpoint, err = url.Parse(conf.Prometheus.Endpoint); err != nil {
		log.Fatal("cannot parse endpoint url, stopping")
		close(stop)
		return
	}

	c, err = remote.NewWriteClient(
		"tapo",
		&remote.ClientConfig{
			URL:     &config.URL{URL: endpoint},
			Timeout: model.Duration(30 * time.Second),
			HTTPClientConfig: config.HTTPClientConfig{
				BasicAuth: &config.BasicAuth{
					Username: conf.Prometheus.Username,
					Password: config.Secret(conf.Prometheus.Password),
				},
			},
			RetryOnRateLimit: true,
		},
	)
	if err != nil {
		log.Fatal("error %s", err)
		close(stop)
		return
	}

	// offset start time by 1 second
	time.Sleep(time.Second)

	ticker := time.NewTicker(time.Duration(conf.Prometheus.FlushInterval) * time.Second)

	var tss []prompb.TimeSeries
	for {
		select {
		case _, ok = <-stop:
			if !ok {
				ticker.Stop()
				log.Info("stopping RemoteWrite")
				wg.Done()
				return
			}

		case ts = <-metrics:
			log.Debug("received time-series")
			tss = append(tss, ts)

		case <-ticker.C:
			log.Debugf("performing batched remote write for %d timeseries", len(tss))
			if len(tss) == 0 {
				continue
			}
			writeReq = &prompb.WriteRequest{Timeseries: tss}
			data, err = proto.Marshal(writeReq)
			if err != nil {
				log.Fatalf("unable to marshal protobuf: %v", err)
				close(stop)
				continue
			}
			encoded = snappy.Encode(nil, data)
			if err = c.Store(context.TODO(), encoded); err != nil {
				if _, ok := err.(remote.RecoverableError); ok {
					log.Infof("recoverable error %s", err.Error())
					continue
				}
				log.Fatalf("error pushing timeseries: %s", err)
				close(stop)
				continue
			}
			log.Infof("pushed %d timeseries", len(tss))
			tss = []prompb.TimeSeries{}
		}
	}

}

func CollectEnergyUsage(wg *sync.WaitGroup, stop chan bool, interval int, c client, metrics chan prompb.TimeSeries) {
	var r map[string]interface{}
	var err error
	var ok bool
	var v float64

	ticker := time.NewTicker(time.Duration(interval) * time.Second)

	for {
		select {
		case _, ok = <-stop:
			if !ok {
				ticker.Stop()
				log.Infof("stopping CollectEnergyUsage %s", c.d.Ip)
				wg.Done()
				return
			}
		case <-ticker.C:
			r, err = c.t.GetEnergyUsage()
			if err != nil {
				log.Warning(err.Error())
			}
			if r["error_code"] != float64(0) {
				log.Warning("non zero error code")
			}
			v = r["result"].(map[string]interface{})["current_power"].(float64)
			metrics <- prompb.TimeSeries{
				Labels: []prompb.Label{
					{Name: "ip", Value: c.d.Ip},
					{Name: "__name__", Value: "current_power"},
				},
				Samples: []prompb.Sample{{
					Timestamp: time.Now().UnixMilli(),
					Value:     v,
				}},
			}
		}
	}
}
