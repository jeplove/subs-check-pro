// 节点裂变脚本
function operator(proxies = []) {
  return proxies.flatMap((p = {}) => {
    const ips = p._resolved_ips
    if (!Array.isArray(ips) || ips.length === 0) return [p]

    const expanded = ips.map((server, i) => ({
      ...p,
      name: + `${p.name}|+${i + 1}`,
      server,
    }))

    if (p._domain) {
      expanded.push({
        ...p,
        name: + `${p.name}|已裂变`,
        server: p._domain,
      })
    }

    return expanded
  })
}