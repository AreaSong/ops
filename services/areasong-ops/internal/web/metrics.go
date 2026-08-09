package web

import (
	"fmt"
	"io"
	"net/http"

	"github.com/AreaSong/ops/services/areasong-ops/internal/buildinfo"
)

func (server *Server) metrics(response http.ResponseWriter, request *http.Request) {
	upstream := server.runnerRequest(request.Context(), http.MethodGet, "/metrics", "", nil, false)
	response.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	response.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(response, "# HELP areasong_ops_build_info 控制面组件构建身份。\n# TYPE areasong_ops_build_info gauge\nareasong_ops_build_info{component=%q,version=%q,revision=%q} 1\n",
		"web", buildinfo.Version, buildinfo.Revision)
	_, _ = io.WriteString(response, "# HELP areasong_ops_web_up Web API 健康状态。\n# TYPE areasong_ops_web_up gauge\nareasong_ops_web_up 1\n")
	reachable := 0
	if upstream != nil {
		defer upstream.Body.Close()
		if upstream.StatusCode == http.StatusOK {
			reachable = 1
			_, _ = io.CopyN(response, upstream.Body, 2<<20)
		}
	}
	_, _ = fmt.Fprintf(response, "# HELP areasong_ops_runner_reachable Web 到 Runner 的连接状态。\n# TYPE areasong_ops_runner_reachable gauge\nareasong_ops_runner_reachable %d\n", reachable)
}
