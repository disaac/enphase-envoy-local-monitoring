// Get Enphase Envoy Solar production data into InfluxDB

// For options:
// > influxEnvoyStats -h

// API path used by the webpage provided by Envoy is e.g.:
//  http://envoy/production.json?details=1

// David Lamb
// 2018-12

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	_ "github.com/influxdata/influxdb1-client" // this is important because of the bug in go mod
	client "github.com/influxdata/influxdb1-client/v2"
	dac "github.com/xinsnake/go-http-digest-auth-client"
)

//EnvoyAPIMeasurement API measurements
type EnvoyAPIMeasurement struct {
	Production  json.RawMessage
	Consumption json.RawMessage
	Storage     json.RawMessage
}

//Inverters Struct for active count in inverters
type Inverters struct {
	ActiveCount int
}

//InvertersReading struct for inverter readings json
type InvertersReading struct {
	DevType         int
	LastReportDate  int64
	LastReportWatts float64
	MaxReportWatts  float64
	SerialNumber    string
}

// Eim struct for EIM json return object
type Eim struct {
	ActiveCount      int
	MeasurementType  string
	ReadingTime      int64
	WNow             float64
	WhLifetime       float64
	VarhLeadLifetime float64
	VarhLagLifetime  float64
	VahLifetime      float64
	RmsCurrent       float64
	RmsVoltage       float64
	ReactPwr         float64
	ApprntPwr        float64
	PwrFactor        float64
	WhToday          float64
	WhLastSevenDays  float64
	VahToday         float64
	VarhLeadToday    float64
	VarhLagToday     float64
}

var (
	envoyHostPtr               = "envoy.local"
	envoyUserName              = "envoy"
	envoyPassword              = "12345"
	influxAddrPtr              = "http://localhost:8086"
	dbNamePtr                  = "db0"
	dbUserPtr                  = "admin"
	dbPwPtr                    = "admin"
	measurementNamePtr         = "readings"
	measurementInverterNamePtr = "inverter_readings"
)

func main() {
	flag.StringVar(&envoyHostPtr, "e", LookupEnvOrString("ENVOY_HOST_PTR", envoyHostPtr), "IP or hostname of Envoy")
	flag.StringVar(&envoyUserName, "eun", LookupEnvOrString("ENVOY_USER_NAME", envoyUserName), "Envoy username")
	flag.StringVar(&envoyPassword, "eps", LookupEnvOrString("ENVOY_PASSWORD", envoyPassword), "Envoy password last 6 digits serial")
	flag.StringVar(&influxAddrPtr, "dba", LookupEnvOrString("INFLUX_ADDR_PTR", influxAddrPtr), "InfluxDB connection address")
	flag.StringVar(&dbNamePtr, "dbn", LookupEnvOrString("DB_NAME_PTR", dbNamePtr), "Influx database name to put readings in")
	flag.StringVar(&dbUserPtr, "dbu", LookupEnvOrString("DB_USER_PTR", dbUserPtr), "DB username")
	flag.StringVar(&dbPwPtr, "dbp", LookupEnvOrString("DB_PW_PTR", dbPwPtr), "DB password")
	flag.StringVar(&measurementNamePtr, "m", LookupEnvOrString("MEASUREMENT_NAME_PTR", measurementNamePtr), "Influx measurement name customisation (table name equivalent)")
	flag.StringVar(&measurementInverterNamePtr, "mi", LookupEnvOrString("MEASUREMENT_INVERTER_NAME_PTR", measurementInverterNamePtr), "Influx inverter measurement name customisation (table name equivalent)")
	flag.Parse()
	log.Println("app.status=starting")
	envoyURL := "http://" + envoyHostPtr + "/production.json?details=1"
	envoyInverterURL := "http://" + envoyHostPtr + "/api/v1/production/inverters"
	envoyClient := http.Client{
		Timeout: time.Second * 4, // Maximum of 2 secs
	}
	req, err := http.NewRequest(http.MethodGet, envoyURL, nil)
	check(err, "envoyURLNewRequest")
	resp, err := envoyClient.Do(req)
	check(err, "envoyClientDoReq")
	jsonData, err := ioutil.ReadAll(resp.Body)
	check(err, "jsonDataReadAll")

	var apiJSONObj struct {
		Production  json.RawMessage
		Consumption json.RawMessage
		Storage     json.RawMessage
	}
	err = json.Unmarshal(jsonData, &apiJSONObj)
	check(err, "jonUnmarshalapiJsonobj")

	inverters := Inverters{}
	prodReadings := Eim{}
	productionObj := []interface{}{&inverters, &prodReadings}
	err = json.Unmarshal(apiJSONObj.Production, &productionObj)
	check(err, "jsonUnmarshalProdObj")

	log.Printf("%d production: %.3f\n", prodReadings.ReadingTime, prodReadings.WNow)

	consumptionReadings := []Eim{}
	err = json.Unmarshal(apiJSONObj.Consumption, &consumptionReadings)
	check(err, "jsonUnmarshalConsumption")
	for _, eim := range consumptionReadings {
		log.Printf("%d %s: %.3f\n", eim.ReadingTime, eim.MeasurementType, eim.WNow)
	}

	// Connect to influxdb specified in commandline arguments
	c, err := client.NewHTTPClient(client.HTTPConfig{
		Addr:     influxAddrPtr,
		Username: dbUserPtr,
		Password: dbPwPtr,
	})
	check(err, "influxDBconnectNewHttpClient")
	defer c.Close()

	bp, err := client.NewBatchPoints(client.BatchPointsConfig{
		Database:  dbNamePtr,
		Precision: "s",
	})
	check(err, "newBatchPointsReadingsConfig")

	readings := append(consumptionReadings, prodReadings)
	for _, reading := range readings {
		tags := map[string]string{
			"type": reading.MeasurementType,
		}
		fields := map[string]interface{}{
			"active_count":       reading.ActiveCount,
			"power_now_watts":    reading.WNow,
			"today_watthours":    reading.WhToday,
			"7days_watthours":    reading.WhLastSevenDays,
			"lifetime_watthours": reading.WhLifetime,
		}
		createdTime := time.Unix(reading.ReadingTime, 0)
		pt, err := client.NewPoint(
			measurementNamePtr,
			tags,
			fields,
			createdTime,
		)
		check(err, "influxdbNewBatchPointNewPoint")
		bp.AddPoint(pt)
	}

	// Write the batch
	err = c.Write(bp)
	check(err, "influxDbBatchPointWriteReadings")
	err = c.Close()
	check(err, "closeInfluxDbConnectionHttp")
	t := dac.NewTransport(envoyUserName, envoyPassword)
	req, err = http.NewRequest(http.MethodGet, envoyInverterURL, nil)
	check(err, "envoyInverterURLnewRequest")
	resp, err = t.RoundTrip(req)
	check(err, "envoyInverterURLRoundTrip")
	defer resp.Body.Close()
	jsonData, err = ioutil.ReadAll(resp.Body)
	check(err, "jsonDataReadAllInverterReadings")
	// var response interface{}
	inverterReadings := []InvertersReading{}

	err = json.Unmarshal(jsonData, &inverterReadings)
	check(err, "jsonUnmarshalInverterReadings")
	inverterLocations := make(map[string]string)
	for _, data := range inverterReadings {
		inverterLocations[data.SerialNumber] = "unknown"
		log.Printf("date:%d\tlocation:%s\tserial:%s\tmaxwats:%.3f\tlastwats:%.3f\n", data.LastReportDate, inverterLocations[data.SerialNumber], data.SerialNumber, data.MaxReportWatts, data.LastReportWatts)
	}

	// Connect to influxdb specified in commandline arguments
	c, err = client.NewHTTPClient(client.HTTPConfig{
		Addr:     influxAddrPtr,
		Username: dbUserPtr,
		Password: dbPwPtr,
	})
	check(err, "influxDbInverterReadingsNewHttpClient")
	defer c.Close()

	bp, err = client.NewBatchPoints(client.BatchPointsConfig{
		Database:  dbNamePtr,
		Precision: "s",
	})
	check(err, "influxdbNewBatchPointNewPointInverterReading")

	for _, reading := range inverterReadings {
		tags := map[string]string{
			"serial":   reading.SerialNumber,
			"location": inverterLocations[reading.SerialNumber],
		}
		fields := map[string]interface{}{
			"last_report_watts": reading.LastReportWatts,
			"max_report_watts":  reading.MaxReportWatts,
		}
		createdTime := time.Unix(reading.LastReportDate, 0)

		pt, err := client.NewPoint(
			measurementInverterNamePtr,
			tags,
			fields,
			createdTime,
		)
		check(err, "influxdbNeNewPointInverterReading")
		bp.AddPoint(pt)
	}

	// Write the batch
	err = c.Write(bp)
	check(err, "influxdbNewBatchPointWriteInverterReading")
	err = c.Close()
	check(err, "influxdbNewBatchPointCloseInverterReading")
}

// LookupEnvOrString Lookup environment variable or set to default
func LookupEnvOrString(key string, defaultVal string) string {
	if val, ok := os.LookupEnv(key); ok {
		return val
	}
	return defaultVal
}

// LookupEnvOrInt Lookup Environment variable Int type or set default
func LookupEnvOrInt(key string, defaultVal int) int {
	if val, ok := os.LookupEnv(key); ok {
		v, err := strconv.Atoi(val)
		if err != nil {
			log.Panicf("LookupEnvOrInt[%s]: %v", key, err)
		}
		return v
	}
	return defaultVal
}

// getConfig grab the configuration from a file
func getConfig(fs *flag.FlagSet) []string {
	cfg := make([]string, 0, 10)
	fs.VisitAll(func(f *flag.Flag) {
		cfg = append(cfg, fmt.Sprintf("%s:%q", f.Name, f.Value.String()))
	})

	return cfg
}

// check for errors
func check(e error, desc string) {
	if e != nil {
		if len(desc) > 0 {
			log.Panicf("%s: %v", desc, e)
		} else {
			log.Panicf("%s: %v", "unknownCall", e)
		}
	}
}
