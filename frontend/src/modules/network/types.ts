// Response shapes of /api/v1/network and /api/v1/firewall.

export interface Gateway {
  family: 'ipv4' | 'ipv6'
  /** Empty for a default route without a next hop. */
  address: string
  interface: string
  metric: number
}

export interface InterfaceStats {
  rx_bytes: number
  tx_bytes: number
  rx_packets: number
  tx_packets: number
  rx_errors: number
  tx_errors: number
  rx_dropped: number
  tx_dropped: number
}

export type InterfaceKind = 'ethernet' | 'wifi' | 'bridge' | 'bond' | 'vlan' | 'virtual' | 'loopback'

export interface NetInterface {
  name: string
  kind: InterfaceKind
  group: 'primary' | 'virtual'
  state: string
  admin_up: boolean
  mac: string | null
  mtu: number
  ipv4: Array<{ address: string; prefix: number }>
  ipv6: Array<{ address: string; prefix: number; scope: 'global' | 'unique_local' | 'link' | 'host' }>
  speed_mbps: number | null
  duplex: 'full' | 'half' | null
  carrier: boolean | null
  link_up_seconds: number | null
  driver: string | null
  device_type: string | null
  master: string | null
  default_route: boolean
  gateways: Gateway[]
  stats: InterfaceStats | null
}

export interface DnsInfo {
  servers: string[]
  search: string[]
  source: string | null
  systemd_resolved: boolean
}

export interface ManagerInfo {
  kind: 'netplan-networkd' | 'netplan-networkmanager' | 'networkmanager' | 'networkd' | 'ifupdown' | 'unknown'
  label: string
  evidence: string[]
  netplan_files: string[]
}

export interface Subnet {
  cidr: string
  interface: string
  family: 'ipv4' | 'ipv6'
  wide: string
}

export interface Listener {
  protocol: 'tcp' | 'udp'
  family: 'ipv4' | 'ipv6'
  address: string
  port: number
  scope: 'all' | 'loopback' | 'specific'
  uid: number
  user: string | null
  process: string | null
}

export interface Overview {
  hostname: string
  interfaces: NetInterface[]
  gateways: Gateway[]
  dns: DnsInfo
  manager: ManagerInfo
  private_subnets: Subnet[]
  listening: Listener[]
  collected_at: number
}

export interface NetplanFile {
  name: string
  size: number
  mode: string
  modified: number
  content: string
  truncated: boolean
  redacted: number
  error: string
}

export interface NetplanView {
  present: boolean
  files: NetplanFile[]
}

export type RuleAction = 'allow' | 'deny' | 'reject' | 'limit'
export type RuleProtocol = 'tcp' | 'udp' | 'any'

export interface FirewallRule {
  number: number
  id: string
  origin: 'numbered' | 'added'
  action: RuleAction
  direction: 'in' | 'out' | 'routed'
  port: string
  protocol: string
  source: string
  source_port: string
  destination: string
  interface: string
  app: string
  comment: string
  ipv6: boolean
  raw: string
  protects: '' | 'ssh' | 'panel'
}

export interface FirewallState {
  installed: boolean
  active: boolean
  defaults: {
    incoming: string | null
    outgoing: string | null
    routed: string | null
    logging: string | null
  }
  rules: FirewallRule[]
  rules_source: 'numbered' | 'added'
  ssh_ports: number[]
  ssh_evidence: string
  panel_port: number
  panel_port_required: boolean
  install_hint: string
  checked_at: number
}

export interface LanSource {
  cidr: string
  kind: 'subnet' | 'block'
  interface: string
}

export interface ServicePreset {
  id: string
  name: string
  port: string
  protocol: RuleProtocol
  lan_only: boolean
}

export interface Suggestions {
  lan_sources: LanSource[]
  services: ServicePreset[]
  client_ip: string
  default_source: string
}

export interface MissingRule {
  kind: 'ssh' | 'panel'
  port: number
  protocol: string
  suggested_source: string
}

export interface EnableCheck {
  installed: boolean
  active: boolean
  can_enable: boolean
  missing: MissingRule[]
  lan_sources: LanSource[]
  client_ip: string
  client_in_lan: boolean
}
