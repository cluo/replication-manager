// +build server

// replication-manager - Replication Manager Monitoring and CLI for MariaDB and MySQL
// Author: Guillaume Lefranc <guillaume@signal18.io>
// License: GNU General Public License, version 3. Redistribution/Reuse of this code is permitted under the GNU v3 license, as an additional term ALL code must carry the original Author(s) credit in comment form.
// See LICENSE in this directory for the integral text.

package main

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	mysqllog "log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"runtime/pprof"
	"strconv"
	"strings"
	"time"

	log "github.com/Sirupsen/logrus"

	"github.com/go-sql-driver/mysql"
	termbox "github.com/nsf/termbox-go"
	uuid "github.com/satori/go.uuid"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/tanji/replication-manager/cluster"
	"github.com/tanji/replication-manager/crypto"
	"github.com/tanji/replication-manager/graphite"
	"github.com/tanji/replication-manager/misc"
	"github.com/tanji/replication-manager/opensvc"
	"github.com/tanji/replication-manager/termlog"
)

// Global variables
var (
	tlog           termlog.TermLog
	termlength     int
	runUUID        string
	repmgrHostname string
	runStatus      string
	splitBrain     bool
	swChan         = make(chan bool)
	exitMsg        string
	exit           bool
	currentCluster *cluster.Cluster
	clusters       = map[string]*cluster.Cluster{}
	agents         []opensvc.Host
	isStarted      bool
)

func getClusterByName(clname string) *cluster.Cluster {
	return clusters[clname]
}

func init() {

	runUUID = uuid.NewV4().String()
	runStatus = "A"
	splitBrain = false
	//conf.FailForceGtid = true
	conf.GoArch = GoArch
	conf.GoOS = GoOS
	//	conf := confs[cfgGroup]
	cobra.OnInitialize(initConfig)

	var errLog = mysql.Logger(mysqllog.New(ioutil.Discard, "", 0))
	mysql.SetLogger(errLog)

	rootCmd.AddCommand(monitorCmd)

	initRepmgrFlags(monitorCmd)
	if GoOS == "linux" {
		monitorCmd.Flags().StringVar(&conf.ShareDir, "monitoring-sharedir", "/usr/share/replication-manager", "Path to HTTP monitor share files")
	}
	if GoOS == "darwin" {
		monitorCmd.Flags().StringVar(&conf.ShareDir, "monitoring-sharedir", "/opt/replication-manager/share", "Path to share files")
	}
	monitorCmd.Flags().StringVar(&conf.WorkingDir, "monitoring-datadir", "/var/lib/replication-manager", "Path to HTTP monitor working directory")
	monitorCmd.Flags().Int64Var(&conf.MonitoringTicker, "monitoring-ticker", 2, "Monitoring time interval in seconds")

	monitorCmd.Flags().StringVar(&conf.User, "db-servers-credential", "", "Database login, specified in the [user]:[password] format")
	monitorCmd.Flags().StringVar(&conf.Hosts, "db-servers-hosts", "", "Database hosts list to monitor, IP and port (optional), specified in the host:[port] format and separated by commas")
	monitorCmd.Flags().StringVar(&conf.HostsTLSCA, "db-servers-tls-ca-cert", "", "Database TLS authority certificate")
	monitorCmd.Flags().StringVar(&conf.HostsTLSKEY, "db-servers-tls-client-key", "", "Database TLS client key")
	monitorCmd.Flags().StringVar(&conf.HostsTLSCLI, "db-servers-tls-client-cert", "", "Database TLS client certificate")
	monitorCmd.Flags().IntVar(&conf.Timeout, "db-servers-connect-timeout", 5, "Database connection timeout in seconds")
	monitorCmd.Flags().IntVar(&conf.ReadTimeout, "db-servers-read-timeout", 15, "Database read timeout in seconds")
	monitorCmd.Flags().StringVar(&conf.PrefMaster, "db-servers-prefered-master", "", "Database preferred candidate in election,  host:[port] format")
	monitorCmd.Flags().StringVar(&conf.IgnoreSrv, "db-servers-ignored-hosts", "", "Database list of hosts to ignore in election")
	monitorCmd.Flags().Int64Var(&conf.SwitchWaitKill, "switchover-wait-kill", 5000, "Switchover wait this many milliseconds before killing threads on demoted master")
	monitorCmd.Flags().IntVar(&conf.SwitchWaitWrite, "switchover-wait-write-query", 10, "Switchover is canceled if a write query is running for this time")
	monitorCmd.Flags().Int64Var(&conf.SwitchWaitTrx, "switchover-wait-trx", 10, "Switchover is cancel after this timeout in second if can't aquire FTWRL")
	monitorCmd.Flags().BoolVar(&conf.SwitchSync, "switchover-at-sync", false, "Switchover Only  when state semisync is sync for last status")
	monitorCmd.Flags().BoolVar(&conf.SwitchGtidCheck, "switchover-at-equal-gtid", false, "Switchover only when slaves are fully in sync")
	monitorCmd.Flags().Int64Var(&conf.SwitchMaxDelay, "switchover-max-slave-delay", 0, "Switchover skip candidate slave if replication passed this delay")

	monitorCmd.Flags().StringVar(&conf.MasterConn, "replication-source-name", "", "Replication channel name to use for multisource")
	monitorCmd.Flags().IntVar(&conf.MasterConnectRetry, "replication-master-connect-retry", 10, "Replication is define using this connection retry timeout")
	monitorCmd.Flags().StringVar(&conf.RplUser, "replication-credential", "", "Replication user in the [user]:[password] format")
	monitorCmd.Flags().BoolVar(&conf.ReplicationSSL, "replication-use-ssl", false, "Replication use SSL encryption to replicate from master")

	monitorCmd.Flags().BoolVar(&conf.MultiMaster, "replication-multi-master", false, "Multi-master topology")
	monitorCmd.Flags().BoolVar(&conf.MultiTierSlave, "replication-multi-tier-slave", false, "Relay slaves topology")

	monitorCmd.Flags().StringVar(&conf.PreScript, "failover-pre-script", "", "Path of pre-failover script")
	monitorCmd.Flags().StringVar(&conf.PostScript, "failover-post-script", "", "Path of post-failover script")

	//	monitorCmd.Flags().BoolVar(&conf.Interactive, "interactive", true, "Ask for user interaction when failures are detected")
	monitorCmd.Flags().BoolVar(&conf.ReadOnly, "failover-readonly-state", true, "Failover Switchover set slaves as read-only")
	monitorCmd.Flags().StringVar(&conf.FailMode, "failover-mode", "manual", "Failover is manual or automatic")
	monitorCmd.Flags().Int64Var(&conf.FailMaxDelay, "failover-max-slave-delay", 0, "Failover ignore slave with replication delay over this time in sec")
	monitorCmd.Flags().BoolVar(&conf.FailRestartUnsafe, "failover-restart-unsafe", false, "Failover when cluster down if a slave is start first ")
	monitorCmd.Flags().IntVar(&conf.FailLimit, "failover-limit", 5, "Failover is canceld if already failover this number of time (0: unlimited)")
	monitorCmd.Flags().Int64Var(&conf.FailTime, "failover-time-limit", 0, "Failover is canceled if timer in sec is not passed with previous failover (0: do not wait)")
	monitorCmd.Flags().BoolVar(&conf.FailSync, "failover-at-sync", false, "Failover only when state semisync is sync for last status")
	monitorCmd.Flags().BoolVar(&conf.FailEventScheduler, "failover-event-scheduler", false, "Failover event scheduler")
	monitorCmd.Flags().BoolVar(&conf.FailEventStatus, "failover-event-status", false, "Failover event status ENABLE OR DISABLE ON SLAVE")
	monitorCmd.Flags().BoolVar(&conf.CheckFalsePositiveHeartbeat, "failover-falsepositive-heartbeat", true, "Failover checks that slaves do not receive hearbeat")
	monitorCmd.Flags().IntVar(&conf.CheckFalsePositiveHeartbeatTimeout, "failover-falsepositive-heartbeat-timeout", 3, "Failover checks that slaves do not receive hearbeat detection timeout ")
	monitorCmd.Flags().BoolVar(&conf.CheckFalsePositiveExternal, "failover-falsepositive-external", false, "Failover checks that http//master:80 does not reponse 200 OK header")
	monitorCmd.Flags().IntVar(&conf.CheckFalsePositiveExternalPort, "failover-falsepositive-external-port", 80, "Failover checks external port")
	monitorCmd.Flags().IntVar(&conf.MaxFail, "failover-falsepositive-ping-counter", 5, "Failover after this number of ping failures (interval 1s)")
	// monitorCmd.Flags().IntVar(&conf.MaxFail, "failcount", 5, "Trigger failover after N ping failures (interval 1s)")
	// monitorCmd.Flags().Int64Var(&conf.FailResetTime, "failcount-reset-time", 300, "Reset failures counter after this time in seconds")

	monitorCmd.Flags().BoolVar(&conf.Autorejoin, "autorejoin", true, "Automatically rejoin a failed server to the current master")
	monitorCmd.Flags().BoolVar(&conf.AutorejoinBackupBinlog, "autorejoin-backup-binlog", true, "Automatically backup ahead binlogs when old master rejoin")
	monitorCmd.Flags().BoolVar(&conf.AutorejoinSemisync, "autorejoin-semisync", true, "Automatically rejoin a failed server to the current master when elected semisync status is SYNC ")
	monitorCmd.Flags().StringVar(&conf.RejoinScript, "autorejoin-script", "", "Path of old master rejoin script")
	monitorCmd.Flags().BoolVar(&conf.AutorejoinFlashback, "autorejoin-flashback", false, "Automatically rejoin a failed server to the current master and flashback at the time of election if new master was delayed")
	monitorCmd.Flags().BoolVar(&conf.AutorejoinMysqldump, "autorejoin-mysqldump", false, "Automatically rejoin a failed server to the current master using mysqldump")

	// monitorCmd.Flags().StringVar(&conf.CheckType, "check-type", "tcp", "Type of server health check (tcp, agent)")
	conf.CheckType = "tcp"
	monitorCmd.Flags().BoolVar(&conf.CheckReplFilter, "check-replication-filters", true, "Check that possible master have equal replication filters")
	monitorCmd.Flags().BoolVar(&conf.CheckBinFilter, "check-binlog-filters", true, "Check that possible master have equal binlog filters")
	monitorCmd.Flags().BoolVar(&conf.RplChecks, "check-replication-state", true, "Check replication status when electing master server")
	monitorCmd.Flags().StringVar(&conf.APIPort, "api-port", "3000", "Rest API listen port")
	monitorCmd.Flags().StringVar(&conf.APIUser, "api-credential", "admin:mariadb", "Rest API user:password")
	monitorCmd.Flags().StringVar(&conf.APIBind, "api-bind", "0.0.0.0", "Rest API bind ip")

	//monitorCmd.Flags().BoolVar(&conf.Daemon, "daemon", true, "Daemon mode. Do not start the Termbox console")
	conf.Daemon = true

	if WithEnforce == "ON" {
		monitorCmd.Flags().BoolVar(&conf.ForceSlaveReadOnly, "force-slave-readonly", false, "Automatically activate read only on slave")
		monitorCmd.Flags().BoolVar(&conf.ForceSlaveHeartbeat, "force-slave-heartbeat", false, "Automatically activate heartbeat on slave")
		monitorCmd.Flags().IntVar(&conf.ForceSlaveHeartbeatRetry, "force-slave-heartbeat-retry", 5, "Replication heartbeat retry on slave")
		monitorCmd.Flags().IntVar(&conf.ForceSlaveHeartbeatTime, "force-slave-heartbeat-time", 3, "Replication heartbeat time")
		monitorCmd.Flags().BoolVar(&conf.ForceSlaveGtid, "force-slave-gtid-mode", false, "Automatically activate gtid mode on slave")
		monitorCmd.Flags().BoolVar(&conf.ForceSlaveNoGtid, "force-slave-no-gtid-mode", false, "Automatically activate no gtid mode on slave")
		monitorCmd.Flags().BoolVar(&conf.ForceSlaveSemisync, "force-slave-semisync", false, "Automatically activate semisync on slave")
		monitorCmd.Flags().BoolVar(&conf.ForceBinlogRow, "force-binlog-row", false, "Automatically activate binlog row format on master")
		monitorCmd.Flags().BoolVar(&conf.ForceBinlogAnnotate, "force-binlog-annotate", false, "Automatically activate annotate event")
		monitorCmd.Flags().BoolVar(&conf.ForceBinlogSlowqueries, "force-binlog-slowqueries", false, "Automatically activate long replication statement in slow log")
		monitorCmd.Flags().BoolVar(&conf.ForceBinlogChecksum, "force-binlog-checksum", false, "Automatically force  binlog checksum")
		monitorCmd.Flags().BoolVar(&conf.ForceBinlogCompress, "force-binlog-compress", false, "Automatically force binlog compression")
		monitorCmd.Flags().BoolVar(&conf.ForceDiskRelayLogSizeLimit, "force-disk-relaylog-size-limit", false, "Automatically limit the size of relay log on disk ")
		monitorCmd.Flags().Uint64Var(&conf.ForceDiskRelayLogSizeLimitSize, "force-disk-relaylog-size-limit-size", 1000000000, "Automatically limit the size of relay log on disk to 1G")
		monitorCmd.Flags().BoolVar(&conf.ForceInmemoryBinlogCacheSize, "force-inmemory-binlog-cache-size", false, "Automatically adapt binlog cache size based on monitoring")
		monitorCmd.Flags().BoolVar(&conf.ForceSyncBinlog, "force-sync-binlog", false, "Automatically force master crash safe")
		monitorCmd.Flags().BoolVar(&conf.ForceSyncInnoDB, "force-sync-innodb", false, "Automatically force master innodb crash safe")
		monitorCmd.Flags().BoolVar(&conf.ForceNoslaveBehind, "force-noslave-behind", false, "Automatically force no slave behing")
	}

	if WithHttp == "ON" {
		monitorCmd.Flags().BoolVar(&conf.HttpServ, "http-server", true, "Start the HTTP monitor")
		monitorCmd.Flags().StringVar(&conf.BindAddr, "http-bind-address", "localhost", "Bind HTTP monitor to this IP address")
		monitorCmd.Flags().StringVar(&conf.HttpPort, "http-port", "10001", "HTTP monitor to listen on this port")
		if GoOS == "linux" {
			monitorCmd.Flags().StringVar(&conf.HttpRoot, "http-root", "/usr/share/replication-manager/dashboard", "Path to HTTP replication-monitor files")
		}
		if GoOS == "darwin" {
			monitorCmd.Flags().StringVar(&conf.HttpRoot, "http-root", "/opt/replication-manager/share/dashboard", "Path to HTTP replication-monitor files")
		}
		monitorCmd.Flags().BoolVar(&conf.HttpAuth, "http-auth", false, "Authenticate to web server")
		monitorCmd.Flags().BoolVar(&conf.HttpBootstrapButton, "http-bootstrap-button", false, "Deprecate for the test flag. Get a boostrap option to init replication")
		monitorCmd.Flags().IntVar(&conf.SessionLifeTime, "http-session-lifetime", 3600, "Http Session life time ")
	}
	if WithMail == "ON" {
		monitorCmd.Flags().StringVar(&conf.MailFrom, "mail-from", "mrm@localhost", "Alert email sender")
		monitorCmd.Flags().StringVar(&conf.MailTo, "mail-to", "", "Alert email recipients, separated by commas")
		monitorCmd.Flags().StringVar(&conf.MailSMTPAddr, "mail-smtp-addr", "localhost:25", "Alert email SMTP server address, in host:[port] format")
	}

	if WithMaxscale == "ON" {
		monitorCmd.Flags().BoolVar(&conf.MxsOn, "maxscale", false, "MaxScale proxy server is query for backend status")
		monitorCmd.Flags().BoolVar(&conf.CheckFalsePositiveMaxscale, "failover-falsepositive-maxscale", false, "Failover checks that maxscale detect failed master")
		monitorCmd.Flags().IntVar(&conf.CheckFalsePositiveMaxscaleTimeout, "failover-falsepositive-maxscale-timeout", 14, "Failover checks that maxscale detect failed master")
		monitorCmd.Flags().BoolVar(&conf.MxsBinlogOn, "maxscale-binlog", false, "Maxscale binlog server topolgy")
		monitorCmd.Flags().MarkDeprecated("maxscale-monitor", "Deprecate disable maxscale monitoring for 2 nodes cluster")
		monitorCmd.Flags().BoolVar(&conf.MxsDisableMonitor, "maxscale-disable-monitor", false, "Disable maxscale monitoring and fully drive server state")
		monitorCmd.Flags().StringVar(&conf.MxsGetInfoMethod, "maxscale-get-info-method", "maxadmin", "How to get infos from Maxscale maxinfo|maxadmin")
		monitorCmd.Flags().StringVar(&conf.MxsHost, "maxscale-servers", "127.0.0.1", "MaxScale hosts ")
		monitorCmd.Flags().StringVar(&conf.MxsPort, "maxscale-port", "6603", "MaxScale admin port")
		monitorCmd.Flags().StringVar(&conf.MxsUser, "maxscale-user", "admin", "MaxScale admin user")
		monitorCmd.Flags().StringVar(&conf.MxsPass, "maxscale-pass", "mariadb", "MaxScale admin password")
		monitorCmd.Flags().IntVar(&conf.MxsWritePort, "maxscale-write-port", 3306, "MaxScale read-write port to leader")
		monitorCmd.Flags().IntVar(&conf.MxsReadPort, "maxscale-read-port", 3307, "MaxScale load balance read port to all nodes")
		monitorCmd.Flags().IntVar(&conf.MxsReadWritePort, "maxscale-read-write-port", 3308, "MaxScale load balance read port to all nodes")
		monitorCmd.Flags().IntVar(&conf.MxsMaxinfoPort, "maxscale-maxinfo-port", 3309, "MaxScale maxinfo plugin http port")
		monitorCmd.Flags().IntVar(&conf.MxsBinlogPort, "maxscale-binlog-port", 3309, "MaxScale maxinfo plugin http port")
		monitorCmd.Flags().BoolVar(&conf.MxsServerMatchPort, "maxscale-server-match-port", false, "Match servers running on same host with different port")
	}
	if WithMariadbshardproxy == "ON" {
		monitorCmd.Flags().BoolVar(&conf.MdbsProxyOn, "mdbshardproxy", false, "Wrapper to use Spider MdbProxy ")
		monitorCmd.Flags().StringVar(&conf.MdbsProxyHosts, "mdbshardproxy-servers", "127.0.0.1:3307", "MariaDB spider proxy hosts IP:Port,IP:Port")
		monitorCmd.Flags().StringVar(&conf.MdbsProxyUser, "mdbshardproxy-user", "admin", "MaxScale admin user")
	}
	if WithHaproxy == "ON" {
		monitorCmd.Flags().BoolVar(&conf.HaproxyOn, "haproxy", false, "Wrapper to use HaProxy on same host")
		monitorCmd.Flags().StringVar(&conf.HaproxyHosts, "haproxy-servers", "127.0.0.1", "HaProxy hosts")
		monitorCmd.Flags().IntVar(&conf.HaproxyWritePort, "haproxy-write-port", 3306, "HaProxy read-write port to leader")
		monitorCmd.Flags().IntVar(&conf.HaproxyReadPort, "haproxy-read-port", 3307, "HaProxy load balance read port to all nodes")
		monitorCmd.Flags().IntVar(&conf.HaproxyStatPort, "haproxy-stat-port", 1988, "HaProxy statistics port")
		monitorCmd.Flags().StringVar(&conf.HaproxyBinaryPath, "haproxy-binary-path", "/usr/sbin/haproxy", "HaProxy binary location")
		monitorCmd.Flags().StringVar(&conf.HaproxyReadBindIp, "haproxy-ip-read-bind", "0.0.0.0", "HaProxy input bind address for read")
		monitorCmd.Flags().StringVar(&conf.HaproxyWriteBindIp, "haproxy-ip-write-bind", "0.0.0.0", "HaProxy input bind address for write")
	}
	if WithProxysql == "ON" {
		monitorCmd.Flags().BoolVar(&conf.ProxysqlOn, "proxysql", false, "Use ProxySQL")
		monitorCmd.Flags().StringVar(&conf.ProxysqlHosts, "proxysql-servers", "127.0.0.1", "ProxySQL hosts")
		monitorCmd.Flags().IntVar(&conf.ProxysqlWritePort, "proxysql-write-port", 3306, "ProxySQL read-write port to leader")
		monitorCmd.Flags().IntVar(&conf.ProxysqlReadPort, "proxysql-read-port", 3307, "ProxySQL load balance read port to all nodes")
		monitorCmd.Flags().IntVar(&conf.ProxysqlStatPort, "proxysql-stat-port", 1988, "ProxySQL statistics port")
		monitorCmd.Flags().StringVar(&conf.ProxysqlBinaryPath, "proxysql-binary-path", "/usr/sbin/proxysql", "ProxySQL binary location")
		monitorCmd.Flags().StringVar(&conf.ProxysqlReadBindIp, "proxysql-ip-read-bind", "0.0.0.0", "HaProxy input bind address for read")
		monitorCmd.Flags().StringVar(&conf.ProxysqlWriteBindIp, "proxysql-ip-write-bind", "0.0.0.0", "HaProxy input bind address for write")
		monitorCmd.Flags().StringVar(&conf.ProxysqlUser, "proxysql-credential", "admin", "MaxScale admin user")
	}
	if WithMonitoring == "ON" {
		monitorCmd.Flags().IntVar(&conf.GraphiteCarbonPort, "graphite-carbon-port", 2003, "Graphite Carbon Metrics TCP & UDP port")
		monitorCmd.Flags().IntVar(&conf.GraphiteCarbonApiPort, "graphite-carbon-api-port", 10002, "Graphite Carbon API port")
		monitorCmd.Flags().IntVar(&conf.GraphiteCarbonServerPort, "graphite-carbon-server-port", 10003, "Graphite Carbon HTTP port")
		monitorCmd.Flags().IntVar(&conf.GraphiteCarbonLinkPort, "graphite-carbon-link-port", 7002, "Graphite Carbon Link port")
		monitorCmd.Flags().IntVar(&conf.GraphiteCarbonPicklePort, "graphite-carbon-pickle-port", 2004, "Graphite Carbon Pickle port")
		monitorCmd.Flags().IntVar(&conf.GraphiteCarbonPprofPort, "graphite-carbon-pprof-port", 7007, "Graphite Carbon Pickle port")
		monitorCmd.Flags().StringVar(&conf.GraphiteCarbonHost, "graphite-carbon-host", "127.0.0.1", "Graphite monitoring host")
		monitorCmd.Flags().BoolVar(&conf.GraphiteMetrics, "graphite-metrics", false, "Enable Graphite monitoring")
		monitorCmd.Flags().BoolVar(&conf.GraphiteEmbedded, "graphite-embedded", false, "Enable Internal Graphite Carbon Server")
	}
	//	monitorCmd.Flags().BoolVar(&conf.Heartbeat, "heartbeat-table", false, "Heartbeat for active/passive or multi mrm setup")
	if WithArbitration == "ON" {
		monitorCmd.Flags().BoolVar(&conf.Arbitration, "arbitration-external", false, "Multi moninitor sas arbitration")
		monitorCmd.Flags().StringVar(&conf.ArbitrationSasSecret, "arbitration-external-secret", "", "Secret for arbitration")
		monitorCmd.Flags().StringVar(&conf.ArbitrationSasHosts, "arbitration-external-hosts", "88.191.151.84:80", "Arbitrator address")
		monitorCmd.Flags().IntVar(&conf.ArbitrationSasUniqueId, "arbitration-external-unique-id", 0, "Unique replication-manager instance idententifier")
		monitorCmd.Flags().StringVar(&conf.ArbitrationPeerHosts, "arbitration-peer-hosts", "127.0.0.1:10002", "Peer replication-manager hosts http port")
		monitorCmd.Flags().StringVar(&conf.DbServerLocality, "db-servers-locality", "127.0.0.1:10002", "List database servers that are in same network locality")
	}
	if WithDeprecate == "ON" {
		monitorCmd.Flags().Int64Var(&conf.SwitchWaitKill, "wait-kill", 5000, "Deprecate for switchover-wait-kill Wait this many milliseconds before killing threads on demoted master")
		monitorCmd.Flags().IntVar(&conf.SwitchWaitWrite, "wait-write-query", 10, "Deprecate  Wait this many seconds before write query end to cancel switchover")
		monitorCmd.Flags().Int64Var(&conf.SwitchWaitTrx, "wait-trx", 10, "Depecrate for switchover-wait-trx Wait this many seconds before transactions end to cancel switchover")
		monitorCmd.Flags().Int64Var(&conf.FailMaxDelay, "maxdelay", 0, "Deprecate Maximum replication delay before initiating failover")
		monitorCmd.Flags().BoolVar(&conf.SwitchGtidCheck, "gtidcheck", false, "Depecrate for failover-at-equal-gtid do not initiate failover unless slaves are fully in sync")
	}

	if WithSpider == "ON" {
		monitorCmd.Flags().BoolVar(&conf.Spider, "spider", false, "Turn on spider detection")
	}
	if WithProvisioning == "ON" {
		monitorCmd.Flags().BoolVar(&conf.Test, "test", false, "Enable non regression tests")
		monitorCmd.Flags().BoolVar(&conf.TestInjectTraffic, "test-inject-traffic", false, "Inject some database traffic via proxy")
		monitorCmd.Flags().IntVar(&conf.SysbenchTime, "sysbench-time", 100, "Time to run benchmark")
		monitorCmd.Flags().IntVar(&conf.SysbenchThreads, "sysbench-threads", 4, "Number of threads to run benchmark")
		monitorCmd.Flags().StringVar(&conf.SysbenchBinaryPath, "sysbench-binary-path", "/usr/bin/sysbench", "Sysbench Wrapper in test mode")
		monitorCmd.Flags().StringVar(&conf.MariaDBBinaryPath, "db-servers-binary-path", "/usr/local/mysql/bin", "Path to mysqld binary for testing")
		monitorCmd.Flags().MarkDeprecated("mariadb-binary-path", "mariadb-binary-path is deprecated, please replace by mariadb-mysqlbinlog-path")
		if WithOpenSVC == "ON" {
			monitorCmd.Flags().BoolVar(&conf.Enterprise, "opensvc", true, "Provisioning via opensvc")
			monitorCmd.Flags().StringVar(&conf.ProvHost, "opensvc-host", "127.0.0.1:443", "OpenSVC collector API")
			monitorCmd.Flags().StringVar(&conf.ProvAdminUser, "opensvc-admin-user", "root@localhost.localdomain:opensvc", "OpenSVC collector admin user")
			monitorCmd.Flags().StringVar(&conf.ProvUser, "opensvc-user", "replication-manager@localhost.localdomain:mariadb", "OpenSVC collector provisioning user")

			monitorCmd.Flags().StringVar(&conf.ProvType, "prov-db-service-type ", "package", "[package|docker]")
			monitorCmd.Flags().StringVar(&conf.ProvAgents, "prov-db-agents", "", "Comma seperated list of agents for micro services provisionning")
			monitorCmd.Flags().StringVar(&conf.ProvMem, "prov-db-memory", "256", "Memory in M for micro service VM")
			monitorCmd.Flags().StringVar(&conf.ProvDisk, "prov-db-disk-size", "20g", "Disk in g for micro service VM")
			monitorCmd.Flags().StringVar(&conf.ProvIops, "prov-db-disk-iops", "300", "Rnd IO/s in for micro service VM")
			monitorCmd.Flags().StringVar(&conf.ProvDbImg, "prov-db-docker-img", "mariadb:latest", "Docker image for database")
			monitorCmd.Flags().StringVar(&conf.ProvTags, "prov-db-tags", "semisync,innodb,noquerycache,threadpool,logslow", "playbook configuration tags")
			monitorCmd.Flags().StringVar(&conf.ProvDiskFS, "prov-db-disk-fs", "ext4", "[zfs|xfs|ext4]")
			monitorCmd.Flags().StringVar(&conf.ProvDiskPool, "prov-db-disk-pool", "none", "[none|zpool|lvm]")
			monitorCmd.Flags().StringVar(&conf.ProvDiskType, "prov-db-disk-type", "[loopback|physical]", "[none|zpool|lvm]")
			monitorCmd.Flags().StringVar(&conf.ProvDiskDevice, "prov-db-disk-device", "[loopback|physical]", "[path-to-loopfile|/dev/xx]")
			monitorCmd.Flags().StringVar(&conf.ProvNetIface, "prov-db-net-iface", "eth0", "HBA Device to hold Ips")
			monitorCmd.Flags().StringVar(&conf.ProvGateway, "prov-db-net-gateway", "192.168.0.254", "Micro Service network gateway")
			monitorCmd.Flags().StringVar(&conf.ProvNetmask, "prov-db-net-mask", "255.255.255.0", "Micro Service network mask")

			monitorCmd.Flags().StringVar(&conf.ProvProxType, "prov-proxy-service-type", "package", "[package|docker]")
			monitorCmd.Flags().StringVar(&conf.ProvProxAgents, "prov-proxy-agents", "", "Comma seperated list of agents for micro services provisionning")
			monitorCmd.Flags().StringVar(&conf.ProvProxDisk, "prov-proxy-disk-size", "20g", "Disk in g for micro service VM")
			monitorCmd.Flags().StringVar(&conf.ProvProxDiskFS, "prov-proxy-disk-fs", "ext4", "[zfs|xfs|ext4]")
			monitorCmd.Flags().StringVar(&conf.ProvProxDiskPool, "prov-proxy-disk-pool", "none", "[none|zpool|lvm]")
			monitorCmd.Flags().StringVar(&conf.ProvProxDiskType, "prov-proxy-disk-type", "[loopback|physical]", "[none|zpool|lvm]")
			monitorCmd.Flags().StringVar(&conf.ProvProxDiskDevice, "prov-proxy-disk-device", "[loopback|physical]", "[path-to-loopfile|/dev/xx]")
			monitorCmd.Flags().StringVar(&conf.ProvProxNetIface, "prov-proxy-net-iface", "eth0", "HBA Device to hold Ips")
			monitorCmd.Flags().StringVar(&conf.ProvProxGateway, "prov-proxy-net-gateway", "192.168.0.254", "Micro Service network gateway")
			monitorCmd.Flags().StringVar(&conf.ProvProxNetmask, "prov-proxy-net-mask", "255.255.255.0", "Micro Service network mask")
			monitorCmd.Flags().StringVar(&conf.ProvProxProxysqlImg, "prov-proxy-docker-proxysql-img", "prima/proxysql:latest", "Docker image for proxysql")
			monitorCmd.Flags().StringVar(&conf.ProvProxMaxscaleImg, "prov-proxy-docker-maxscale-img", "asosso/maxscale:latest", "Docker image for maxscale proxy")
			monitorCmd.Flags().StringVar(&conf.ProvProxHaproxyImg, "prov-proxy-docker-haproxy-img", "haproxy:latest", "Docker image for haproxy")
		}
	}

	viper.BindPFlags(monitorCmd.Flags())
	//	viper.RegisterAlias("mariadb-binary-path", "mariadb-mysqlbinlog-path")

	var err error
	repmgrHostname, err = os.Hostname()
	if err != nil {
		log.Fatalln("ERROR: replication-manager could not get hostname from system")
	}

}

// initRepmgrFlags function is used to initialize flags that are common to several subcommands
// e.g. monitor, failover, switchover.
// If you add a subcommand that shares flags with other subcommand scenarios please call this function.
// If you add flags that impact all the possible scenarios please do it here.
func initRepmgrFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&conf.LogFile, "logfile", "", "Write output messages to log file")
	viper.BindPFlags(cmd.Flags())

}

var monitorCmd = &cobra.Command{
	Use:   "monitor",
	Short: "Starts monitoring server",
	Long: `Starts replication-manager server in stateful monitor daemon mode.

For interacting with this daemon use,
- Interactive console client: "replication-manager client".
- Command line clients: "replication-manager switchover|failover|topology|test".
- HTTP dashboards on port 10001

`,
	Run: func(cmd *cobra.Command, args []string) {

		if conf.FailMode == "manual" {
			conf.Interactive = true
		} else {
			conf.Interactive = false
		}
		if conf.LogLevel > 1 {
			log.SetLevel(log.DebugLevel)
		}
		if conf.Arbitration == true {
			runStatus = "S"
		}
		if !conf.Daemon {
			err := termbox.Init()
			if err != nil {
				log.WithError(err).Fatal("Termbox initialization error")
			}
		}
		if conf.Daemon {
			termlength = 40
			log.WithField("version", Version).Info("replication-manager started in daemon mode")
		} else {
			_, termlength = termbox.Size()
			if termlength == 0 {
				termlength = 120
			} else if termlength < 18 {
				log.Fatal("Terminal too small, please increase window size")
			}
		}
		loglen := termlength - 9 - (len(strings.Split(conf.Hosts, ",")) * 3)
		tlog = termlog.NewTermLog(loglen)

		go apiserver()

		var svc opensvc.Collector
		if conf.Enterprise {
			svc.Host, svc.Port = misc.SplitHostPort(conf.ProvHost)
			svc.User, svc.Pass = misc.SplitPair(conf.ProvAdminUser)
			svc.RplMgrUser, svc.RplMgrPassword = misc.SplitPair(conf.ProvUser)
			//don't Bootstrap opensvc to speedup test
			if !conf.Test {
				err := svc.Bootstrap(conf.ShareDir + "/opensvc/")
				if err != nil {
					log.Printf("%s", err)
				}
			}
			agents = svc.GetNodes()
		}
		// Initialize go-carbon
		if conf.GraphiteEmbedded {
			go graphite.RunCarbon(conf.ShareDir, conf.WorkingDir, conf.GraphiteCarbonPort, conf.GraphiteCarbonLinkPort, conf.GraphiteCarbonPicklePort, conf.GraphiteCarbonPprofPort, conf.GraphiteCarbonServerPort)
			log.WithFields(log.Fields{
				"metricport": conf.GraphiteCarbonPort,
				"httpport":   conf.GraphiteCarbonServerPort,
			}).Info("Carbon server started")
			time.Sleep(2 * time.Second)
			go graphite.RunCarbonApi("http://0.0.0.0:"+strconv.Itoa(conf.GraphiteCarbonServerPort), conf.GraphiteCarbonApiPort, 20, "mem", "", 200, 0, "", conf.WorkingDir)
			log.WithField("apiport", conf.GraphiteCarbonApiPort).Info("Carbon server API started")
		}

		// If there's an existing encryption key, decrypt the passwords
		k, err := readKey()
		if err != nil {
			log.WithError(err).Info("No existing password encryption scheme")
			k = nil
		}
		apiUser, apiPass = misc.SplitPair(conf.APIUser)
		if k != nil {
			p := crypto.Password{Key: k}
			p.CipherText = apiPass
			p.Decrypt()
			apiPass = p.PlainText
		}
		for _, gl := range cfgGroupList {
			currentCluster = new(cluster.Cluster)
			currentCluster.Init(confs[gl], gl, &tlog, termlength, runUUID, Version, repmgrHostname, k)
			clusters[gl] = currentCluster
			go currentCluster.Run()
			currentClusterName = gl
		}
		currentCluster.SetCfgGroupDisplay(currentClusterName)

		// HTTP server should start after Cluster Init or may lead to various nil pointer if clients still requesting
		if conf.HttpServ {
			go httpserver()
		}
		interval := time.Second
		ticker := time.NewTicker(interval * time.Duration(conf.MonitoringTicker))
		isStarted = true
		for exit == false {

			select {
			case <-ticker.C:
				if conf.Arbitration {
					fHeartbeat()
				}
				if conf.Enterprise {
					//			agents = svc.GetNodes()
				}
			}

		}
		if exitMsg != "" {
			log.Println(exitMsg)
		}
	},
	PostRun: func(cmd *cobra.Command, args []string) {
		// Close connections on exit.
		currentCluster.Close()
		termbox.Close()
		if memprofile != "" {
			f, err := os.Create(memprofile)
			if err != nil {
				log.Fatal(err)
			}
			pprof.WriteHeapProfile(f)
			f.Close()
		}
	},
}

func newTbChan() chan termbox.Event {
	termboxChan := make(chan termbox.Event)
	go func() {
		for {
			termboxChan <- termbox.PollEvent()
		}
	}()
	return termboxChan
}

func fHeartbeat() {
	if cfgGroup == "arbitrator" {
		currentCluster.LogPrintf("ERROR", "Arbitrator cannot send heartbeat to itself. Exiting")
		return
	}
	bcksplitbrain := splitBrain

	var peerList []string
	// try to found an active peer replication-manager
	if conf.ArbitrationPeerHosts != "" {
		peerList = strings.Split(conf.ArbitrationPeerHosts, ",")
	} else {
		currentCluster.LogPrintf("ERROR", "Arbitration peer not specified. Disabling arbitration")
		conf.Arbitration = false
		return
	}
	splitBrain = true
	timeout := time.Duration(2 * time.Second)
	for _, peer := range peerList {
		url := "http://" + peer + "/heartbeat"
		client := &http.Client{
			Timeout: timeout,
		}
		// Send the request via a client
		// Do sends an HTTP request and
		// returns an HTTP response
		// Build the request
		currentCluster.LogPrintf("DEBUG", "Heartbeat: Sending peer request to node %s", peer)
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			if bcksplitbrain == false {
				currentCluster.LogPrintf("ERROR", "Error building HTTP request: %s", err)
			}
			continue

		}
		resp, err := client.Do(req)
		if err != nil {
			if bcksplitbrain == false {
				currentCluster.LogPrintf("ERROR", "Could not reach peer node, might be down or incorrect address")
			}
			continue
		}
		defer resp.Body.Close()
		monjson, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			currentCluster.LogPrintf("ERROR", "Could not read body from peer response")
		}
		// Use json.Decode for reading streams of JSON data
		var h heartbeat
		if err := json.Unmarshal(monjson, &h); err != nil {
			currentCluster.LogPrintf("ERROR", "Could not unmarshal JSON from peer response", err)
		} else {
			splitBrain = false
			if conf.LogLevel > 1 {
				currentCluster.LogPrintf("DEBUG", "RETURN: %v", h)
			}
			if h.Status == "S" {
				currentCluster.LogPrintf("DEBUG", "Peer node is Standby, I am Active")
				runStatus = "A"
			} else {
				currentCluster.LogPrintf("DEBUG", "Peer node is Active, I am Standby")
				runStatus = "S"
			}
		}

	}
	if splitBrain {
		if bcksplitbrain != splitBrain {
			currentCluster.LogPrintf("INFO", "Arbitrator: Splitbrain")
		}

		// report to arbitrator
		for _, cl := range clusters {
			if cl.LostMajority() {
				if bcksplitbrain != splitBrain {
					currentCluster.LogPrintf("INFO", "Arbitrator: Database cluster lost majority")
				}
			}
			url := "http://" + conf.ArbitrationSasHosts + "/heartbeat"
			var mst string
			if cl.GetMaster() != nil {
				mst = cl.GetMaster().URL
			}
			var jsonStr = []byte(`{"uuid":"` + runUUID + `","secret":"` + conf.ArbitrationSasSecret + `","cluster":"` + cl.GetName() + `","master":"` + mst + `","id":` + strconv.Itoa(conf.ArbitrationSasUniqueId) + `,"status":"` + runStatus + `","hosts":` + strconv.Itoa(len(cl.GetServers())) + `,"failed":` + strconv.Itoa(cl.CountFailed(cl.GetServers())) + `}`)
			req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonStr))
			req.Header.Set("X-Custom-Header", "myvalue")
			req.Header.Set("Content-Type", "application/json")

			client := &http.Client{Timeout: timeout}
			currentCluster.LogPrintf("DEBUG", "Sending message to Arbitrator server")
			resp, err := client.Do(req)
			if err != nil {
				cl.LogPrintf("ERROR", "Could not get http response from Arbitrator server")
				cl.SetActiveStatus("S")
				runStatus = "S"
				return
			}
			defer resp.Body.Close()

		}
		// give a chance to other partitions to report if just happened
		if bcksplitbrain != splitBrain {
			time.Sleep(5 * time.Second)
		}
		// request arbitration for all cluster
		for _, cl := range clusters {

			if bcksplitbrain != splitBrain {
				cl.LogPrintf("INFO", "Arbitrator: External check requested")
			}
			url := "http://" + conf.ArbitrationSasHosts + "/arbitrator"
			var mst string
			if cl.GetMaster() != nil {
				mst = cl.GetMaster().URL
			}
			var jsonStr = []byte(`{"uuid":"` + runUUID + `","secret":"` + conf.ArbitrationSasSecret + `","cluster":"` + cl.GetName() + `","master":"` + mst + `","id":` + strconv.Itoa(conf.ArbitrationSasUniqueId) + `,"status":"` + runStatus + `","hosts":` + strconv.Itoa(len(cl.GetServers())) + `,"failed":` + strconv.Itoa(cl.CountFailed(cl.GetServers())) + `}`)
			req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonStr))
			req.Header.Set("X-Custom-Header", "myvalue")
			req.Header.Set("Content-Type", "application/json")

			client := &http.Client{Timeout: timeout}
			resp, err := client.Do(req)
			if err != nil {
				cl.LogPrintf("ERROR", "Could not get http response from Arbitrator server")
				cl.SetActiveStatus("S")
				cl.SetMasterReadOnly()
				runStatus = "S"
				return
			}
			defer resp.Body.Close()

			body, _ := ioutil.ReadAll(resp.Body)

			type response struct {
				Arbitration string `json:"arbitration"`
				Master      string `json:"master"`
			}
			var r response
			err = json.Unmarshal(body, &r)
			if err != nil {
				cl.LogPrintf("ERROR", "Arbitrator received invalid JSON")
				cl.SetActiveStatus("S")
				cl.SetMasterReadOnly()
				runStatus = "S"
				return

			}
			if r.Arbitration == "winner" {
				if bcksplitbrain != splitBrain {
					cl.LogPrintf("INFO", "Arbitration message - Election Won")
				}
				cl.SetActiveStatus("A")
				runStatus = "A"
				return
			}
			if bcksplitbrain != splitBrain {
				cl.LogPrintf("INFO", "Arbitration message - Election Lost")
				if cl.GetMaster() != nil {
					mst = cl.GetMaster().URL
				}
				if r.Master != mst {
					cl.SetMasterReadOnly()
				}
			}
			cl.SetActiveStatus("S")
			runStatus = "S"
			return

		}

	}

}
