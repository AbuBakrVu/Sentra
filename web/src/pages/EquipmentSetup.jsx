import AddIP from './AddIP.jsx'
import AddServer from './AddServer.jsx'
import AddProxmox from './AddProxmox.jsx'
import AddPBS from './AddPBS.jsx'
import AddVMware from './AddVMware.jsx'
import AddSNMP from './AddSNMP.jsx'
import { InternetSetup } from './Internet.jsx'

export function equipmentSetup(path, inDialog = false) {
  const snmpDevice = path.match(/^\/add\/snmp\/(mikrotik|usergate|nas|other)$/)?.[1]
  if (snmpDevice) return <AddSNMP key={snmpDevice} initialDevice={snmpDevice} inDialog={inDialog} />

  switch (path) {
    case '/add/ip': return <AddIP inDialog={inDialog} />
    case '/add/linux': return <AddServer inDialog={inDialog} />
    case '/add/proxmox': return <AddProxmox inDialog={inDialog} />
    case '/add/pbs': return <AddPBS inDialog={inDialog} />
    case '/add/vmware': return <AddVMware inDialog={inDialog} />
    case '/add/vmware/esxi': return <AddVMware key="esxi" initialTarget="esxi" inDialog={inDialog} />
    case '/add/snmp': return <AddSNMP inDialog={inDialog} />
    case '/internet': return inDialog ? <InternetSetup /> : null
    default: return null
  }
}
