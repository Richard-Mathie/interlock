package stats

import (
	"fmt"
	"net"
	"os"
	"regexp"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/ehazlett/interlock"
	"github.com/ehazlett/interlock/plugins"
	"github.com/samalba/dockerclient"
)

const (
	defaultImageNameRegex = ".*"
)

var (
	errorChan = make(chan error)
)

type StatsPlugin struct {
	interlockConfig *interlock.Config
	pluginConfig    *PluginConfig
	client          *dockerclient.DockerClient
}

func init() {
	plugins.Register(
		pluginInfo.Name,
		&plugins.RegisteredPlugin{
			New: NewPlugin,
			Info: func() *interlock.PluginInfo {
				return pluginInfo
			},
		})
}

func loadPluginConfig() (*PluginConfig, error) {
	defaultImageNameFilter := regexp.MustCompile(defaultImageNameRegex)

	cfg := &PluginConfig{
		CarbonAddress:   "",
		StatsPrefix:     "docker.stats",
		ImageNameFilter: defaultImageNameFilter,
	}

	// load custom config via environment
	carbonAddress := os.Getenv("STATS_CARBON_ADDRESS")
	if carbonAddress != "" {
		cfg.CarbonAddress = carbonAddress
	}

	statsPrefix := os.Getenv("STATS_PREFIX")
	if statsPrefix != "" {
		cfg.StatsPrefix = statsPrefix
	}

	imageNameFilter := os.Getenv("STATS_IMAGE_NAME_FILTER")
	if imageNameFilter != "" {
		// validate regex
		r, err := regexp.Compile(imageNameFilter)
		if err != nil {
			return nil, err
		}
		cfg.ImageNameFilter = r
	}

	return cfg, nil
}

func NewPlugin(interlockConfig *interlock.Config, client *dockerclient.DockerClient) (interlock.Plugin, error) {
	p := StatsPlugin{interlockConfig: interlockConfig, client: client}
	cfg, err := loadPluginConfig()
	if err != nil {
		return nil, err
	}
	p.pluginConfig = cfg
	return p, nil
}

func (p StatsPlugin) handleStats(id string, cb dockerclient.StatCallback, ec chan error, args ...interface{}) {
	go p.client.StartMonitorStats(id, cb, ec, args)
}

func (p StatsPlugin) Info() *interlock.PluginInfo {
	return pluginInfo
}

func (p StatsPlugin) sendStat(path string, value interface{}, t *time.Time) error {
	conn, err := net.Dial("tcp", p.pluginConfig.CarbonAddress)
	if err != nil {
		return err
	}
	defer conn.Close()

	timestamp := t.Unix()
	v := fmt.Sprintf("%s %v %d", path, value, timestamp)
	plugins.Log(pluginInfo.Name, log.DebugLevel,
		fmt.Sprintf("sending to carbon: %v", v),
	)
	fmt.Fprintf(conn, "%s\n", v)

	return nil
}

func (p StatsPlugin) sendEventStats(id string, stats *dockerclient.Stats, ec chan error, args ...interface{}) {
	if len(id) > 12 {
		id = id[:12]
	}
	statBasePath := p.pluginConfig.StatsPrefix + ".containers." + id
	type containerStat struct {
		Key   string
		Value interface{}
	}
	statData := []containerStat{
		{
			Key:   "cpu.usage.totalUsage",
			Value: stats.CpuStats.CpuUsage.TotalUsage,
		},
		{
			Key:   "cpu.usage.usageInKernelmode",
			Value: stats.CpuStats.CpuUsage.UsageInKernelmode,
		},
		{
			Key:   "cpu.usage.usageInUsermode",
			Value: stats.CpuStats.CpuUsage.UsageInUsermode,
		},
		{
			Key:   "memory.stats.usage",
			Value: stats.MemoryStats.Usage,
		},
		{
			Key:   "memory.stats.maxUsage",
			Value: stats.MemoryStats.MaxUsage,
		},
		{
			Key:   "memory.stats.failcnt",
			Value: stats.MemoryStats.Failcnt,
		},
		{
			Key:   "memory.stats.limit",
			Value: stats.MemoryStats.Limit,
		},
		{
			Key:   "network.rxBytes",
			Value: stats.Network.RxBytes,
		},
		{
			Key:   "network.rxPackets",
			Value: stats.Network.RxPackets,
		},
		{
			Key:   "network.rxErrors",
			Value: stats.Network.RxErrors,
		},
		{
			Key:   "network.rxDropped",
			Value: stats.Network.RxDropped,
		},
		{
			Key:   "network.txBytes",
			Value: stats.Network.TxBytes,
		},
		{
			Key:   "network.txPackets",
			Value: stats.Network.TxPackets,
		},
		{
			Key:   "network.txErrors",
			Value: stats.Network.TxErrors,
		},
		{
			Key:   "network.txDropped",
			Value: stats.Network.TxDropped,
		},
	}

	timestamp := time.Now()
	for _, s := range statData {
		plugins.Log(pluginInfo.Name,
			log.DebugLevel,
			fmt.Sprintf("stat t=%d id=%s key=%s value=%v",
				timestamp,
				id,
				s.Key,
				s.Value,
			),
		)
		m := fmt.Sprintf("%s.%s", statBasePath, s.Key)
		plugins.Log(pluginInfo.Name, log.DebugLevel, m)
		if err := p.sendStat(m, s.Value, &timestamp); err != nil {
			ec <- err
		}
	}

	return
}

func (p StatsPlugin) HandleEvent(event *dockerclient.Event) error {
	t := time.Now()
	if err := p.sendStat(p.pluginConfig.StatsPrefix+".all.events", 1, &t); err != nil {
		plugins.Log(pluginInfo.Name, log.ErrorLevel, err.Error())
	}
	if event.Status == "start" {
		// get container info for event
		c, err := p.client.InspectContainer(event.Id)
		if err != nil {
			return err
		}

		fmt.Println(c.Config.Image)
		// match regex to start monitoring
		if p.pluginConfig.ImageNameFilter.MatchString(c.Config.Image) {
			plugins.Log(pluginInfo.Name, log.DebugLevel,
				fmt.Sprintf("gathering stats: image=%s id=%s", c.Image, c.Id[:12]))
			go p.handleStats(event.Id, p.sendEventStats, errorChan, nil)
		}
	}
	return nil
}
