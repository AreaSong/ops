// stage6d:topology:begin v1
// 仅供离线候选组装；返回原结构不代表可应用，必须通过候选验收。
function simplifyLosAngelesNode(config) {
  const nodes = config.proxies;
  const groups = config["proxy-groups"];
  if (!Array.isArray(nodes) || !Array.isArray(groups)) return;
  const mainNodes = nodes.filter(isLosAngelesNode);
  if (mainNodes.length !== 1) return;
  const node = mainNodes[0];
  const name = "🇺🇸 洛杉矶｜VMess · WS · TLS";
  const backup = "🇸🇬 新加坡｜E-IX 01";
  const primary = "🚀 节点选择";
  const intermediate = "🚀 手动切换";
  const names = [...nodes, ...groups].map(item => item.name);
  if (new Set(names).size !== names.length || groups.some(g => g.name === name)) return;
  if (nodes.some(n => n !== node && (n.name !== backup || !isSingaporeNode(n)))) return;
  if (node.name !== name && names.includes(name)) return;
  const alias = groups.find(group => group.name === intermediate);
  if (alias && !isSingleNodeAlias(alias, node.name)) return;
  const oldNames = new Set([node.name, intermediate]);
  const candidate = {
    ...config,
    proxies: nodes.map(item => item === node ? {...item, name} : item),
    "proxy-groups": groups.filter(group => group.name !== intermediate)
      .map(group => replaceSingleNodeReferences(group, oldNames, name, backup)),
    dns: replaceDnsOutbound(config.dns, oldNames, name),
  };
  const main = candidate["proxy-groups"].find(group => group.name === primary);
  if (!main || !main.proxies?.includes(name)) return;
  // 只接受已知节点/DIRECT 的旧主组；未知候选不被静默删除。
  if (main.proxies.some(value => ![name, backup, "DIRECT"].includes(value))) return;
  main.proxies = [name, ...nodes.filter(item => item !== node).map(item => item.name)];
  // 剩余的旧别名（规则、dialer 或扩展字段）不能用模糊替换掩盖。
  const serialized = JSON.stringify(candidate);
  if ([...oldNames].some(old => old !== name
    && (serialized.includes(old) || serialized.includes(encodeURIComponent(old))))) return;
  Object.assign(config, candidate);
}

function isLosAngelesNode(node) {
  return node && node.server === "23.185.200.12" && node.port === 443
    && node.type === "vmess" && node.network === "ws" && node.tls === true
    && node.servername === "log.areasong.top" && node["skip-cert-verify"] === false
    && node["ws-opts"]?.path === "/as"
    && node["ws-opts"]?.headers?.Host === "log.areasong.top";
}

function isSingaporeNode(node) {
  return node && node.type === "anytls" && node.server === "8e3f7b290d64.eixcloud.com"
    && node.port === 43121 && node.sni === "rds.emsfi.com"
    && node["client-fingerprint"] === "chrome" && node.udp === true
    && node["skip-cert-verify"] === false
    && JSON.stringify(node.alpn) === JSON.stringify(["h2", "http/1.1"]);
}

function isSingleNodeAlias(group, nodeName) {
  return group.type === "select"
    && Array.isArray(group.proxies) && group.proxies.length === 1
    && group.proxies[0] === nodeName
    && Object.keys(group).every(key => ["name", "type", "proxies"].includes(key));
}

function replaceSingleNodeReferences(group, oldNames, nodeName, backup) {
  if (!Array.isArray(group.proxies)) return group;
  const primary = "🚀 节点选择";
  const followers = ["💬 Ai平台", "📲 电报消息", "📹 油管视频", "🎥 奈飞视频",
    "📺 巴哈姆特", "🌍 国外媒体", "📢 谷歌FCM", "🐟 漏网之鱼"];
  let proxies = [...new Set(group.proxies.map(name => oldNames.has(name) ? nodeName : name))];
  if (group.name === primary) return {...group, proxies};
  if (followers.includes(group.name)) {
    // 未知引用保留给验收器拒收，不能在收口时消失。
    if (proxies.some(name => ![primary, nodeName, backup, "DIRECT"].includes(name))) return group;
    proxies = [primary];
  } else {
    proxies = [...new Set(proxies.map(name => [nodeName, backup].includes(name) ? primary : name))];
  }
  return {...group, proxies};
}

function replaceDnsOutbound(value, oldNames, nodeName) {
  if (!value || typeof value !== "object" || Array.isArray(value)) return value;
  const dns = {...value};
  const policy = value["nameserver-policy"];
  if (policy && typeof policy === "object" && !Array.isArray(policy)) {
    dns["nameserver-policy"] = {...policy};
    for (const key of ["geosite:geolocation-!cn", "geosite:gfw"]) {
      if (Object.prototype.hasOwnProperty.call(policy, key)) {
        dns["nameserver-policy"][key] = replaceDnsServers(policy[key], oldNames, nodeName);
      }
    }
  }
  if (Object.prototype.hasOwnProperty.call(value, "fallback")) {
    dns.fallback = replaceDnsServers(value.fallback, oldNames, nodeName);
  }
  return dns;
}

function replaceDnsServers(value, oldNames, nodeName) {
  if (Array.isArray(value)) return value.map(item => replaceDnsServers(item, oldNames, nodeName));
  if (typeof value !== "string" || !value.includes("#")) return value;
  const marker = value.indexOf("#");
  const [outbound, ...options] = value.slice(marker + 1).split("&");
  let decoded;
  try { decoded = decodeURIComponent(outbound); } catch { return value; }
  if (![...oldNames, nodeName, "🚀 节点选择"].includes(decoded)) return value;
  // 只替换三处境外解析的出口，保留服务、查询串及片段附加参数。
  return value.slice(0, marker + 1) + [encodeURIComponent("🚀 节点选择"), ...options].join("&");
}
// stage6d:topology:end v1
