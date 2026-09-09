// The drawn height of a node card. The edge geometry and the cluster layout both
// depend on it, and they must not drift from the CSS min-height.
const TOPOLOGY_NODE_HEIGHT = 96;

const state = { overview: null, devices: null, topology: null, networkScan: null, channelScan: null, deviceMode: 'map', deviceQuery: '', roleFilter: 'all', topologySelectedId: '', expandedDevices: new Set(), busy: false, networkScanBusy: false, renamingExt: null, network: null };
const fields = [...document.querySelectorAll('[data-field]')];
const statusDots = [...document.querySelectorAll('[data-status-dot]')];
const statusLabels = [...document.querySelectorAll('[data-status-label]')];
const refreshDot = document.querySelector('[data-refresh-dot]');

const glossary = {
  'thread-network': {
    title: 'What the OTBR does',
    description: 'An OpenThread Border Router connects the low-power Thread IPv6 mesh to your Ethernet or Wi-Fi network. It advertises routes and services, supports secure commissioning, and lets Thread devices communicate with adjacent IP networks.',
    why: 'The Thread mesh keeps working as a distributed network; the border router is its IP bridge, not a central hub that every local packet must cross. OTBR Insight monitors that bridge and the network data it exposes.'
  },
  'nearby-networks': {
    title: 'Nearby Thread networks',
    description: 'Separate Thread networks whose discovery beacons were heard by this border router during an active radio scan. They are not devices or routers inside the network this OTBR is currently attached to.',
    why: 'This view helps identify other Thread deployments and possible channel overlap. The CURRENT badge marks the scan result that matches this OTBR’s attached network.'
  },
  role: {
    title: 'Thread role',
    description: 'A node can be the leader, a router, a child, detached, or disabled. Routers forward mesh traffic; children attach through a parent router. The leader is a router with additional coordination duties.',
    why: 'Role shows how a node currently participates in the mesh. It can change as the network adapts.'
  },
  'router-count': {
    title: 'Active routers',
    description: 'The number of router-role nodes participating in this Thread partition. It does not include every end device or sleepy child.',
    why: 'Routers provide the mesh backbone and forwarding paths. A higher count can improve coverage, although Thread limits and manages the router set automatically.'
  },
  'parent-link': {
    title: 'Parent link',
    description: 'An end device attaches through one parent router. The relationship can be reported directly by OTBR or determined from the router portion of the child’s RLOC16.',
    why: 'Parent links explain how children enter the mesh. They are not permanent: a child can reattach to a different router as radio conditions change.'
  },
  'partition-id': {
    title: 'Partition ID',
    description: 'A 32-bit identifier for the currently connected Thread partition. A partition is a group of nodes that can reach one another and share the same leader and network data.',
    why: 'Nodes with the same network credentials can temporarily form separate partitions. Different Partition IDs reveal that split; the ID may change when partitions form or merge.'
  },
  'otbr-api': {
    title: 'OTBR REST API',
    description: 'The local interface OTBR Insight reads to obtain border-router and Thread network status.',
    why: 'A healthy API means this dashboard can talk to OTBR. It does not by itself prove that every radio link or Thread device is healthy.'
  },
  'border-agent': {
    title: 'Border Agent',
    description: 'The OTBR service that lets an external Thread Commissioner securely reach the mesh during commissioning and management.',
    why: 'An active Border Agent makes it possible for authorized tools and apps to add devices without exposing Thread network credentials directly.'
  },
  'leader-router': {
    title: 'Leader router',
    description: 'One router in each partition becomes the leader and maintains the authoritative Thread network data used by other routers.',
    why: 'The leader coordinates the partition, but it is not a single traffic gateway or permanent master. Another router can take over if needed.'
  },
  'router-id': {
    title: 'Router ID',
    description: 'A compact identifier assigned to a router inside the current partition. It forms the upper portion of that router’s RLOC16.',
    why: 'Router IDs describe the current topology and can be reassigned; they should not be used as durable device identities.'
  },
  rloc16: {
    title: 'RLOC16',
    description: 'A 16-bit Routing Locator that represents where a node sits in the current Thread topology. For a child, it encodes both its parent router and its child number.',
    why: 'RLOC16 is excellent for routing and troubleshooting, but it can change when a device reattaches. Use the extended address when you need a more stable identity.'
  },
  'extended-address': {
    title: 'Extended address',
    description: 'A 64-bit IEEE 802.15.4 MAC address, often shown as an EUI-64, that identifies the Thread radio interface.',
    why: 'Unlike RLOC16, this value is not derived from the node’s current place in the topology, so it is a better identifier when tracking a device over time.'
  },
  'extended-pan-id': {
    title: 'Extended PAN ID',
    description: 'A 64-bit identifier stored in the active Thread dataset that helps distinguish this Thread network from others nearby.',
    why: 'It identifies the network but is not a secret credential. The network key and PSKc remain sensitive and are never displayed here.'
  },
  'pan-id': {
    title: 'PAN ID',
    description: 'A 16-bit IEEE 802.15.4 identifier used by this Thread network on its current radio channel.',
    why: 'It helps radios distinguish nearby personal-area networks. It is a local network identifier, not a secret credential or a durable device identity.'
  },
  'parent': {
    title: 'Parent',
    description: 'The router an end device is attached to. A child talks to the mesh only through its parent, which buffers traffic for it while it sleeps.',
    why: 'A child picks its parent when it attaches and stays until the link fails, so a weak parent link persists until the device re-attaches. Routers have no parent.'
  },
  'channel-noise': {
    title: 'Channel noise',
    description: 'For about ten seconds the border router repeatedly listens briefly on each of the sixteen 2.4 GHz channels. The bar is the strongest signal heard on a channel across all those passes — from Wi-Fi, Bluetooth, Zigbee and other Thread networks alike — and the line is the typical level. Lower is quieter.',
    why: 'Thread shares the band with Wi-Fi. Channels 15, 20, 25 and 26 sit between the three common Wi-Fi channels, which is why they are the usual choices. Compare bars within one measurement rather than reading them as absolutes: the figure depends on how long the radio listened. Changing channel re-attaches every device, so only move for a clear difference.'
  },
  'omr-ipv6': {
    title: 'OMR IPv6 address',
    description: 'An Off-Mesh Routable IPv6 address allocated from the Thread network’s OMR prefix. Border routers advertise a route to that prefix on adjacent IP links.',
    why: 'This is the address class used when Thread devices communicate beyond the mesh through a border router.'
  },
  'link-local': {
    title: 'IPv6 link-local address',
    description: 'An IPv6 address in fe80::/10 that is valid only on the directly attached network link and is not routed beyond it.',
    why: 'Link-local addressing supports local IPv6 control traffic and neighbor discovery even when no routable prefix is available.'
  },
  'mesh-local': {
    title: 'Mesh-local addressing',
    description: 'The Thread network’s private IPv6 prefix and addresses used for communication within the mesh.',
    why: 'Mesh-local addresses stay inside the Thread mesh. They are separate from OMR addresses, which border routers make reachable from adjacent IP networks.'
  },
  'rloc-address': {
    title: 'RLOC IPv6 address',
    description: 'A mesh-local IPv6 address whose final portion contains the node’s current RLOC16 topology locator.',
    why: 'It is useful for routing and diagnostics, but it can change when the node’s role or attachment changes.'
  },
  rcp: {
    title: 'Radio Co-Processor',
    description: 'The RCP is the radio-side component that handles IEEE 802.15.4 radio operations while OpenThread and border-routing services run on the host.',
    why: 'Its state, hardware EUI-64, and firmware version help diagnose compatibility and radio-layer problems separately from the host software.'
  },
  'wpan-service': {
    title: 'WPAN service',
    description: 'The operating state of the local wireless personal-area network interface managed by OTBR.',
    why: 'Associated means the local Thread interface is attached to a Thread network; it does not guarantee that every remote device is reachable.'
  },
  'thread-channel': {
    title: 'Thread radio channel',
    description: 'The IEEE 802.15.4 channel currently used by the Thread network in the 2.4 GHz band.',
    why: 'Channel choice affects coexistence with Wi-Fi and other 2.4 GHz networks and is useful when investigating interference.'
  },
  'openthread-api': {
    title: 'OpenThread API version',
    description: 'A build-reported compatibility version for the OpenThread software interface used by this OTBR installation.',
    why: 'It helps compare installations and diagnose feature or compatibility differences between OpenThread builds.'
  },
  'device-role': {
    title: 'Device role',
    description: 'The device’s current job in the Thread mesh: leader, router, router-eligible end device, or child such as a minimal or sleepy end device.',
    why: 'Roles affect forwarding, power use, and attachment behavior, and may change without changing the physical device.'
  },
  'link-metrics': {
    title: 'Link metrics',
    description: 'Radio measurements such as RSSI in dBm, Link Quality Indicator (LQI), or link margin. Higher link margin and LQI generally indicate a more reliable connection; RSSI values closer to zero are stronger.',
    why: 'They help diagnose weak radio paths. A blank value means the current OTBR device collection did not report diagnostics, not necessarily that the link is down.'
  },
  observed: {
    title: 'Observed time',
    description: 'The most recent discovery or update time supplied by the inventory data source.',
    why: 'It indicates data freshness, but it is not guaranteed to be the exact time the device last sent a Thread packet.'
  }
};

let activeTermTrigger = null;

function openTerm(key, trigger = null) {
  const term = glossary[key];
  if (!term) return;
  if (activeTermTrigger) activeTermTrigger.setAttribute('aria-expanded', 'false');
  activeTermTrigger = trigger;
  if (activeTermTrigger) activeTermTrigger.setAttribute('aria-expanded', 'true');
  document.getElementById('termTitle').textContent = term.title;
  document.getElementById('termDescription').textContent = term.description;
  document.getElementById('termWhy').textContent = term.why;
  document.getElementById('termPopover').classList.remove('hidden');
}

function closeTerm(returnFocus = false) {
  const popover = document.getElementById('termPopover');
  if (popover.classList.contains('hidden')) return;
  popover.classList.add('hidden');
  if (activeTermTrigger) {
    activeTermTrigger.setAttribute('aria-expanded', 'false');
    if (returnFocus) activeTermTrigger.focus();
  }
  activeTermTrigger = null;
}

function decorateTerms() {
  document.querySelectorAll('[data-term]').forEach(label => {
    const term = glossary[label.dataset.term];
    if (!term || label.querySelector('.term-help-button')) return;
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'term-help-button';
    button.textContent = '?';
    button.setAttribute('aria-label', `Explain ${term.title}`);
    button.setAttribute('aria-haspopup', 'dialog');
    button.setAttribute('aria-expanded', 'false');
    button.addEventListener('click', event => {
      event.stopPropagation();
      openTerm(label.dataset.term, button);
    });
    label.append(button);
  });
}

const ICON_PATHS = {
  hexagon: '<path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/>',
  router: '<path d="M12 3l9 9-9 9-9-9z" fill="currentColor" stroke="none"/>',
  endDevice: '<path d="M12 3l9 9-9 9-9-9z"/>',
  mesh: '<circle cx="18" cy="5" r="3"/><circle cx="6" cy="12" r="3"/><circle cx="18" cy="19" r="3"/><line x1="8.59" y1="13.51" x2="15.42" y2="17.49"/><line x1="15.41" y1="6.51" x2="8.59" y2="10.49"/>',
  radio: '<circle cx="12" cy="12" r="2"/><path d="M16.24 7.76a6 6 0 0 1 0 8.49m-8.48-.01a6 6 0 0 1 0-8.49m11.31-2.82a10 10 0 0 1 0 14.14m-14.14 0a10 10 0 0 1 0-14.14"/>'
};

function makeIcon(name) {
  const template = document.createElement('template');
  template.innerHTML = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${ICON_PATHS[name]}</svg>`;
  return template.content.firstElementChild;
}

const formatters = {
  routerCount: value => value ?? '—',
  routerId: value => value ?? '—',
  leaderRouterId: value => value ?? '—',
  partitionId: value => value == null ? '—' : String(value),
};

function displayValue(key, value) {
  if (formatters[key]) return formatters[key](value);
  return value === undefined || value === null || value === '' ? '—' : value;
}

function renderOverview(data) {
  state.overview = data;
  fields.forEach(node => {
    const value = displayValue(node.dataset.field, data[node.dataset.field]);
    node.textContent = value;
    if (node.classList.contains('mono') || node.classList.contains('wrap-value')) node.title = value;
  });
  const status = data.status || 'connecting';
  const disabled = status === 'online' && data.state === 'disabled';
  let label, dotClass;
  if (status === 'offline') {
    label = 'OTBR offline';
    dotClass = 'offline';
  } else if (status !== 'online') {
    label = 'Connecting';
    dotClass = 'connecting';
  } else if (disabled) {
    label = 'Network disabled';
    dotClass = 'disabled';
  } else {
    label = data.stale ? 'Online · stale data' : 'Online · healthy';
    dotClass = 'online';
  }
  statusDots.forEach(dot => { dot.className = `status-dot ${dotClass}`; });
  statusLabels.forEach(node => node.textContent = label);

  const alert = document.getElementById('healthAlert');
  if (status === 'offline') {
    alert.classList.remove('hidden');
    document.getElementById('alertTitle').textContent = data.hasData ? 'OTBR connection interrupted' : 'OTBR is unavailable';
    document.getElementById('alertText').textContent = data.hasData ? 'Showing the last valid snapshot while OTBR Insight reconnects.' : 'Check that OTBR is running and its REST API is reachable.';
  } else {
    alert.classList.add('hidden');
  }

  const title = document.getElementById('pageTitle');
  if (!['#network', '#help', '#manage', '#diagnostics'].includes(window.location.hash)) title.textContent = homeTitle();

  updateRefreshLabel();
}

function roleCategory(role = '') {
  const value = role.toLowerCase().replaceAll('_', '-').replaceAll(' ', '-');
  if (value === 'router' || value === 'leader' || value === 'border-router') return 'router';
  if (['child', 'sed', 'med', 'reed', 'end-device', 'sleepy-end-device'].includes(value)) return 'end-device';
  return 'unknown';
}

function renderDevices(data) {
  state.devices = data;
  const items = data.items || [];
  const status = data.status || 'connecting';
  document.getElementById('inventorySource').textContent = `${data.source || 'Unknown source'}${data.requestLatencyMs != null ? ` · ${data.requestLatencyMs} ms` : ''}`;

  const note = document.getElementById('inventoryNote');
  note.classList.remove('neutral');
  if (status === 'unavailable') {
    note.classList.remove('hidden');
    note.querySelector('p').textContent = data.stale ? 'The latest refresh failed. Showing the most recent valid inventory.' : 'The device inventory could not be read from OTBR.';
  } else if (!data.collectionSupported && status !== 'connecting') {
    note.classList.remove('hidden');
    note.querySelector('p').textContent = 'This OTBR version does not expose its full device collection. The local border router is shown; other nodes remain unavailable until another provider is added.';
  } else {
    note.classList.add('hidden');
  }
  renderDeviceRows();
  updateRefreshLabel();
}

// renderTopologyData is the last of the three per-poll renders, so it is the one
// that draws the map: the map reads overview and inventory too, and drawing it
// from each would rebuild it three times per poll.
function renderTopologyData(data) {
  state.topology = data;
  renderTopology();
  updateRefreshLabel();
}

function setDeviceMode(mode) {
  state.deviceMode = mode === 'list' ? 'list' : 'map';
  if (state.deviceMode !== 'map') closeDevicePopover(false);
  document.querySelectorAll('[data-device-mode]').forEach(button => {
    const selected = button.dataset.deviceMode === state.deviceMode;
    button.classList.toggle('active', selected);
    button.setAttribute('aria-pressed', String(selected));
  });
  document.querySelectorAll('[data-device-panel]').forEach(panel => {
    panel.classList.toggle('hidden', panel.dataset.devicePanel !== state.deviceMode);
  });
  updateRefreshLabel();
  if (state.deviceMode === 'map') window.requestAnimationFrame(renderTopology);
}

function normalizedPANID(value) {
  const clean = String(value || '').trim().toLowerCase().replace(/^0x/, '').replace(/^0+/, '');
  return clean || '0';
}

function isCurrentNetwork(network) {
  const overview = state.overview || {};
  const panMatches = network.panId && overview.panId && normalizedPANID(network.panId) === normalizedPANID(overview.panId);
  const channelMatches = network.channel == null || overview.rcpChannel == null || Number(network.channel) === Number(overview.rcpChannel);
  const xpanMatches = network.extendedPanId && overview.extendedPanId && network.extendedPanId.toLowerCase() === overview.extendedPanId.toLowerCase();
  return Boolean((panMatches && channelMatches) || xpanMatches);
}

function networkFact(label, value) {
  const fact = document.createElement('div');
  fact.className = 'network-card-fact';
  const heading = document.createElement('span');
  const content = document.createElement('strong');
  heading.textContent = label;
  content.textContent = value === undefined || value === null || value === '' ? 'Not advertised' : String(value);
  content.title = content.textContent;
  fact.append(heading, content);
  return fact;
}

function networkDetail(label, value) {
  const item = document.createElement('div');
  const heading = document.createElement('dt');
  const content = document.createElement('dd');
  heading.textContent = label;
  content.textContent = value || 'Not advertised';
  content.title = content.textContent;
  item.append(heading, content);
  return item;
}

function renderNetworkScan(data) {
  state.networkScan = data;
  const items = data.items || [];
  const status = data.status || 'unavailable';
  const badge = document.getElementById('networkScanStatus');
  badge.textContent = status === 'available' ? `${items.length} FOUND` : status === 'scanning' ? 'SCANNING' : status === 'unsupported' ? 'UNSUPPORTED' : 'UNAVAILABLE';
  badge.className = `inventory-badge ${status === 'available' ? '' : status}`.trim();

  const note = document.getElementById('networkScanNote');
  note.classList.remove('scan-error');
  if (status === 'unavailable' || status === 'unsupported') {
    note.classList.add('scan-error');
    note.querySelector('p').textContent = data.error || 'The available network scan could not be completed.';
    note.classList.remove('hidden');
  } else if (items.some(network => !network.name || !network.extendedPanId)) {
    note.querySelector('p').textContent = 'Some nearby beacons did not advertise a network name or Extended PAN ID. Channel, PAN ID, and hardware address are still shown.';
    note.classList.remove('hidden');
  } else if (data.passes > 1) {
    note.querySelector('p').textContent = `Merged from ${data.passes} discovery passes: neighbours answer intermittently, so one pass alone misses about half of them.`;
    note.classList.remove('hidden');
  } else {
    note.classList.add('hidden');
  }

  // The page knows its own network from the overview, so its card should not
  // depend on another router in it answering a discovery request — with a single
  // other router, that answer comes and goes. When the scan did not hear it,
  // show it anyway and say so.
  const overview = state.overview || {};
  const heardOwn = items.some(isCurrentNetwork);
  const shown = items.slice();
  if (status === 'available' && !heardOwn && overview.networkName) {
    shown.unshift({
      name: overview.networkName, extendedPanId: overview.extendedPanId, panId: overview.panId,
      channel: overview.rcpChannel != null ? Number(overview.rcpChannel) : null, hardwareAddress: '', unheard: true
    });
  }
  const rows = document.getElementById('networkRows');
  if (!shown.length) {
    const empty = document.createElement('div');
    empty.className = 'inventory-empty';
    const icon = document.createElement('span');
    icon.className = 'network-scan-symbol';
    icon.replaceChildren(makeIcon('radio'));
    const title = document.createElement('strong');
    title.textContent = status === 'available' ? 'No Thread networks found' : 'Network scan unavailable';
    const text = document.createElement('p');
    text.textContent = status === 'available' ? 'No discoverable Thread beacons were heard during this scan.' : 'Try again after checking the OTBR web service and radio state.';
    empty.append(icon, title, text);
    rows.replaceChildren(empty);
  } else {
    rows.replaceChildren(...shown.map((network, index) => {
      const current = network.unheard || isCurrentNetwork(network);
      const card = document.createElement('article');
      card.className = `network-card ${current ? 'current' : ''}`;
      card.setAttribute('role', 'listitem');
      const header = document.createElement('div');
      header.className = 'network-card-header';
      const icon = document.createElement('i');
      icon.textContent = current ? '●' : '○';
      const identity = document.createElement('span');
      const name = document.createElement('strong');
      name.textContent = network.name || (current ? state.overview?.networkName : '') || `Unnamed network ${index + 1}`;
      const detail = document.createElement('small');
      detail.textContent = network.unheard ? 'Your network — no other router in it answered this scan' : current ? 'Attached to this OTBR' : 'Heard during this scan';
      identity.append(name, detail);
      const relationship = document.createElement('span');
      relationship.className = `network-tag ${current ? 'current' : ''}`;
      relationship.textContent = current ? 'CURRENT' : 'NEARBY';
      header.append(icon, identity, relationship);

      const facts = document.createElement('div');
      facts.className = 'network-card-facts';
      facts.append(
        networkFact('Channel', network.channel == null ? null : network.channel),
        networkFact('PAN ID', network.panId)
      );

      const more = document.createElement('div');
      more.className = 'network-card-details';
      const list = document.createElement('dl');
      list.append(
        networkDetail('Extended PAN ID', network.extendedPanId),
        networkDetail('Hardware address', network.unheard ? 'Not heard in this scan' : network.hardwareAddress)
      );
      more.append(list);
      card.append(header, facts, more);
      return card;
    }));
  }
  updateRefreshLabel();
}

// Wi-Fi overlap: each 20 MHz Wi-Fi channel covers four 802.15.4 channels. The
// gaps between Wi-Fi 1, 6 and 11 are Thread channels 15, 20, 25 and 26.
const WIFI_BANDS = [{ label: 'Wi-Fi 1', from: 11, to: 14 }, { label: 'Wi-Fi 6', from: 16, to: 19 }, { label: 'Wi-Fi 11', from: 21, to: 24 }];

// channelLevel grades a channel against the quietest in the same scan; the
// absolute figure depends on dwell time, so only the spread is meaningful.
function channelLevel(rssi, quietest) {
  const delta = rssi - quietest;
  return delta <= 3 ? 'quiet' : delta > 10 ? 'busy' : 'moderate';
}

function renderChannelScan(data) {
  state.channelScan = data;
  const channels = (data.channels || []).slice().sort((a, b) => a.channel - b.channel);
  const status = data.status || 'unavailable';
  const note = document.getElementById('channelScanNote');
  const chart = document.getElementById('channelChart');
  note.classList.remove('scan-error');

  if (status !== 'available' || !channels.length) {
    note.classList.add('scan-error');
    note.querySelector('p').textContent = data.error || 'The channel measurement could not be completed.';
    note.classList.remove('hidden');
    const empty = document.createElement('div');
    empty.className = 'inventory-empty';
    const title = document.createElement('strong');
    title.textContent = 'Channel measurement unavailable';
    const text = document.createElement('p');
    text.textContent = status === 'unsupported' ? 'Neither the daemon socket nor this OTBR build offers an energy scan.' : 'Try again in a moment.';
    empty.append(title, text);
    chart.replaceChildren(empty);
    return;
  }

  const quietest = Math.min(...channels.map(ch => ch.maxRssi));
  const current = data.currentChannel ?? (state.overview?.rcpChannel != null ? Number(state.overview.rcpChannel) : null);
  // The current channel's loudest reading includes this network's own frames,
  // which no other channel can show, so it is graded on its typical level.
  const typicals = channels.filter(ch => ch.typicalRssi != null).map(ch => ch.typicalRssi);
  const quietestTypical = typicals.length ? Math.min(...typicals) : null;
  const gradeOf = ch => (ch.channel === current && quietestTypical != null && ch.typicalRssi != null)
    ? channelLevel(ch.typicalRssi, quietestTypical)
    : channelLevel(ch.maxRssi, quietest);
  const heard = new Map();
  (state.networkScan?.items || []).forEach(network => {
    if (network.channel != null) heard.set(Number(network.channel), (heard.get(Number(network.channel)) || 0) + 1);
  });

  const columns = channels.map(ch => {
    const level = gradeOf(ch);
    const column = document.createElement('div');
    column.className = `channel-col ${level} ${ch.channel === current ? 'current' : ''}`.trim();
    const rssi = document.createElement('span');
    rssi.className = 'channel-rssi';
    rssi.textContent = String(ch.maxRssi);
    const track = document.createElement('div');
    track.className = 'channel-track';
    const bar = document.createElement('div');
    bar.className = 'channel-bar';
    // Absolute scale so two measurements look alike: -105 dBm is the floor, -40 the top.
    const heightFor = rssi => Math.round(Math.min(1, Math.max(0.04, (rssi + 105) / 65)) * 100);
    bar.style.height = `${heightFor(ch.maxRssi)}%`;
    track.append(bar);
    // The bar is the loudest reading across the sweeps; the line is the typical
    // one, so a single burst reads as a tall bar with a low line.
    if (ch.typicalRssi != null) {
      const typical = document.createElement('span');
      typical.className = 'channel-typical';
      typical.style.bottom = `${heightFor(ch.typicalRssi)}%`;
      track.append(typical);
    }
    const number = document.createElement('span');
    number.className = 'channel-num';
    number.textContent = String(ch.channel);
    const dot = document.createElement('span');
    const count = heard.get(ch.channel) || 0;
    dot.className = `channel-heard ${count ? '' : 'none'}`.trim();
    if (count) dot.title = `${count} Thread network${count === 1 ? '' : 's'} heard on this channel in the last scan`;
    column.append(rssi, track, number, dot);
    const overlap = WIFI_BANDS.find(band => ch.channel >= band.from && ch.channel <= band.to);
    column.title = `Channel ${ch.channel}: loudest ${ch.maxRssi} dBm${ch.typicalRssi != null ? `, typically ${ch.typicalRssi} dBm` : ''} (${level})${overlap ? `, overlaps ${overlap.label}` : ', between Wi-Fi channels'}${ch.channel === current ? ' — current channel' : ''}`;
    return column;
  });

  const wifi = document.createElement('div');
  wifi.className = 'channel-wifi';
  const first = channels[0].channel;
  WIFI_BANDS.forEach(band => {
    const span = document.createElement('span');
    span.textContent = band.label;
    span.style.gridColumn = `${band.from - first + 1} / span ${band.to - band.from + 1}`;
    wifi.append(span);
  });

  const legend = document.createElement('div');
  legend.className = 'channel-legend';
  const legendItems = [['', 'Quiet (within 3 dB of the quietest)'], ['moderate', 'Moderate'], ['busy', 'Busy (more than 10 dB above)'], ['heard', 'Thread network heard here']];
  if (channels.some(ch => ch.typicalRssi != null)) legendItems.push(['typical', 'Line: typical level · bar: loudest']);
  legendItems.forEach(([cls, label]) => {
    const item = document.createElement('span');
    const swatch = document.createElement('i');
    swatch.className = cls;
    item.append(swatch, label);
    legend.append(item);
  });

  chart.replaceChildren(...columns, wifi, legend);

  const ranked = channels.slice().sort((a, b) => a.maxRssi - b.maxRssi || a.channel - b.channel).slice(0, 3).map(ch => ch.channel);
  let summary = `Quietest: ${ranked.slice(0, -1).join(', ')} and ${ranked[ranked.length - 1]}.`;
  if (data.sweeps > 1) summary = `Loudest of ${data.sweeps} sweeps; candidates ranked by their worst reading. ` + summary;
  const currentEntry = channels.find(ch => ch.channel === current);
  if (currentEntry && quietestTypical != null && currentEntry.typicalRssi != null) {
    const delta = currentEntry.typicalRssi - quietestTypical;
    summary += delta <= 3 ? ` Your network is on channel ${current}, which is typically at the noise floor, so it is fine — its loudest reading includes your own devices' traffic, which no other channel can show.`
      : delta > 10 ? ` Your network is on channel ${current}, typically ${delta} dB above the quietest, so it is noisy most of the time — worth considering a change, though every device re-attaches afterwards.`
      : ` Your network is on channel ${current}, typically ${delta} dB above the quietest; not worth a change on its own.`;
  } else if (currentEntry) {
    const delta = currentEntry.maxRssi - quietest;
    summary += delta <= 3 ? ` Your network is on channel ${current}, one of the quietest.`
      : delta > 10 ? ` Your network is on channel ${current}, ${delta} dB noisier than the quietest — worth considering a change, though every device re-attaches afterwards.`
      : ` Your network is on channel ${current}, ${delta} dB above the quietest; not worth a change on its own.`;
  }
  note.querySelector('p').textContent = summary;
  note.classList.remove('hidden');
  updateRefreshLabel();
}

async function scanChannels() {
  if (state.channelScanBusy) return;
  state.channelScanBusy = true;
  const button = document.getElementById('scanChannelsButton');
  button.disabled = true;
  button.classList.add('scanning');
  button.lastChild.textContent = ' Measuring (about 10 s)…';
  try {
    const response = await fetch('/api/v1/channels', { cache: 'no-store' });
    const payload = await response.json();
    renderChannelScan(payload.data);
  } catch (error) {
    renderChannelScan({ status: 'unavailable', channels: [], error: 'The measurement request did not complete.' });
  } finally {
    state.channelScanBusy = false;
    button.disabled = false;
    button.classList.remove('scanning');
    button.lastChild.textContent = ' Measure';
  }
}

async function scanNetworks() {
  if (state.networkScanBusy) return;
  state.networkScanBusy = true;
  const button = document.getElementById('scanNetworksButton');
  const badge = document.getElementById('networkScanStatus');
  button.disabled = true;
  button.classList.add('scanning');
  button.lastChild.textContent = ' Scanning…';
  badge.textContent = 'SCANNING';
  badge.className = 'inventory-badge scanning';
  try {
    const response = await fetch('/api/v1/networks', { cache: 'no-store' });
    const payload = await response.json();
    renderNetworkScan(payload.data);
  } catch (error) {
    renderNetworkScan({ status: 'unavailable', items: [], source: 'OTBR active scan', error: 'The scan request could not reach the OTBR web service.' });
  } finally {
    state.networkScanBusy = false;
    button.disabled = false;
    button.classList.remove('scanning');
    button.lastChild.textContent = ' Scan now';
  }
}

function topologyDevices() {
  if (state.topology?.collectionSupported && (state.topology.nodes || []).length) {
    return state.topology.nodes.map(node => {
      const inventoryDevice = (state.devices?.items || []).find(device => node.extendedAddress && (device.id === node.extendedAddress || device.extendedAddress === node.extendedAddress));
      return {
        ...(inventoryDevice || {}),
        ...node,
        name: node.name || inventoryDevice?.name || (node.role === 'child' ? `Child ${node.rloc16 || node.id.split('/').at(-1)}` : ''),
        parent: node.parentId,
        ipv6Addresses: [...(inventoryDevice?.ipv6Addresses || [])],
        topologyDiagnostic: true
      };
    });
  }
  const items = (state.devices?.items || []).map(device => ({ ...device, ipv6Addresses: [...(device.ipv6Addresses || [])] }));
  const overview = state.overview;
  const hasLocal = items.some(device => device.isBorderRouter);
  if (!hasLocal && overview?.hasData && overview.extendedAddress) {
    items.unshift({
      id: overview.extendedAddress,
      name: 'Border Router',
      role: overview.role,
      rloc16: overview.rloc16,
      routerId: overview.routerId,
      extendedAddress: overview.extendedAddress,
      omrIpv6Address: overview.omrIpv6Address,
      ipv6Addresses: overview.omrIpv6Address ? [overview.omrIpv6Address] : [],
      isBorderRouter: true
    });
  }
  return items;
}

function topologyKey(value) {
  return String(value ?? '').trim().toLowerCase().replace(/^0x/, '').replace(/[^a-z0-9]/g, '');
}

function routerIdFromRloc(rloc16) {
  if (!rloc16) return null;
  const clean = String(rloc16).trim().toLowerCase().replace(/^0x/, '');
  if (!/^[0-9a-f]{1,4}$/.test(clean)) return null;
  const value = Number.parseInt(clean, 16);
  return Number.isNaN(value) ? null : (value & 0xfc00) >> 10;
}

function isTopologyRouter(device) {
  if (device.isBorderRouter || roleCategory(device.role) === 'router') return true;
  const rloc16 = String(device.rloc16 || '').trim().toLowerCase().replace(/^0x/, '');
  if (!/^[0-9a-f]{1,4}$/.test(rloc16)) return false;
  const value = Number.parseInt(rloc16, 16);
  return !Number.isNaN(value) && (value & 0x03ff) === 0;
}

function topologyParent(device, routers) {
  const parent = topologyKey(device.parent);
  if (parent) {
    const direct = routers.find(router => [router.id, router.extendedAddress, router.rloc16, router.routerId].some(value => topologyKey(value) === parent));
    if (direct) return { device: direct, source: device.topologyDiagnostic ? 'OTBR child table' : 'Reported by OTBR' };
    const numericParent = /^\d+$/.test(parent) ? Number.parseInt(parent, 10) : null;
    if (numericParent != null) {
      const byRouterId = routers.find(router => (router.routerId ?? routerIdFromRloc(router.rloc16)) === numericParent);
      if (byRouterId) return { device: byRouterId, source: 'Reported router ID' };
    }
  }
  const routerId = routerIdFromRloc(device.rloc16);
  if (routerId == null) return null;
  const derived = routers.find(router => (router.routerId ?? routerIdFromRloc(router.rloc16)) === routerId);
  return derived ? { device: derived, source: 'Derived from RLOC16' } : null;
}

// topologyGrid lays `count` nodes out in centred rows of at most `columns`. Wrapping
// is what keeps the stage inside the viewport: a single unwrapped row of 36 children
// made the stage 6380px wide, leaving under a fifth of the map on screen.
function topologyGrid(count, columns, width, top, rowPitch) {
  const nodeWidth = 154;
  const columnPitch = 180;
  return Array.from({ length: count }, (_, index) => {
    const row = Math.floor(index / columns);
    const column = index % columns;
    const inThisRow = Math.min(columns, count - row * columns);
    const rowWidth = inThisRow * columnPitch;
    const start = Math.max(24, Math.round((width - rowWidth) / 2 + (columnPitch - nodeWidth) / 2));
    return { x: start + column * columnPitch, y: top + row * rowPitch };
  });
}

function topologyEdge(from, to, source, type = 'child') {
  let x1 = from.x + 77;
  let y1 = from.y + TOPOLOGY_NODE_HEIGHT;
  let x2 = to.x + 77;
  let y2 = to.y;
  if (type === 'router') {
    const left = from.x <= to.x ? from : to;
    const right = from.x <= to.x ? to : from;
    x1 = left.x + 154;
    y1 = left.y + 41;
    x2 = right.x;
    y2 = right.y + 41;
  }
  // Child edges stop short of the target so the arrowhead is not drawn under the node card.
  const length = Math.max(0, Math.hypot(x2 - x1, y2 - y1) - (type === 'child' ? 7 : 0));
  const edge = document.createElement('span');
  edge.className = `topology-edge ${type}`;
  edge.style.left = `${x1}px`;
  edge.style.top = `${y1}px`;
  edge.style.width = `${length}px`;
  edge.style.transform = `rotate(${Math.atan2(y2 - y1, x2 - x1)}rad)`;
  edge.title = source;
  return edge;
}

function topologyLayerLabel(text, y) {
  const label = document.createElement('span');
  label.className = 'topology-layer-label';
  label.style.top = `${y}px`;
  label.textContent = text;
  return label;
}

function topologyNode(device, position, attachment = null) {
  const category = isTopologyRouter(device) ? 'router' : roleCategory(device.role);
  const leader = String(device.role || '').toLowerCase() === 'leader' || (isTopologyRouter(device) && device.routerId != null && device.routerId === state.overview?.leaderRouterId);
  const node = document.createElement('button');
  node.type = 'button';
  node.className = `topology-node ${category} ${leader ? 'leader' : ''} ${device.isBorderRouter ? 'local' : ''} ${state.topologySelectedId === device.id ? 'selected' : ''}`.trim();
  node.style.left = `${position.x}px`;
  node.style.top = `${position.y}px`;
  node.setAttribute('aria-label', `Inspect ${deviceLabel(device, device.id || 'Thread device')}`);

  const top = document.createElement('span');
  top.className = 'topology-node-top';
  const icon = document.createElement('i');
  icon.replaceChildren(makeIcon(device.isBorderRouter ? 'hexagon' : category === 'router' ? 'router' : 'endDevice'));
  const badges = document.createElement('span');
  if (leader) badges.append(topologyBadge('LEADER', 'leader'));
  if (device.isBorderRouter) badges.append(topologyBadge('LOCAL', 'local'));
  top.append(icon, badges);

  const label = deviceLabel(device, device.isBorderRouter ? 'Border Router' : device.id || 'Thread device');
  const name = document.createElement('strong');
  name.textContent = label;
  node.append(top, name);
  // Derived child labels already read "Child 0x9401", so the detail line adds only
  // what the name does not already say — falling back to link quality when it says everything.
  const lower = label.toLowerCase();
  const role = String(device.role || '').toLowerCase();
  // Only the derived "Child 0x9401" form leads with its role; a user's "Kitchen Router"
  // merely contains the word, and still deserves the role spelled out.
  const nameLeadsWithRole = Boolean(role) && (lower === role || lower.startsWith(role + ' '));
  const parts = [];
  if (device.role && !nameLeadsWithRole) parts.push(device.role);
  if (device.rloc16 && !lower.includes(String(device.rloc16).toLowerCase())) parts.push(device.rloc16);
  // Link margin rather than LQI: the 0-3 bucket puts a 21 dB link and a 39 dB one
  // in the same bin, hiding exactly the difference that explains retry rates.
  if (!parts.length && device.linkMargin != null) parts.push(`${device.linkMargin} dB margin`);
  else if (!parts.length && device.linkQuality != null) parts.push(`LQI ${device.linkQuality}`);
  if (parts.length) {
    const detail = document.createElement('small');
    detail.textContent = parts.join(' · ');
    node.append(detail);
  }
  // Signal and recency, the two things you actually want per device on a map.
  // Only nodes with a measured link get this: the local border router has no link
  // to itself, and a bare "just now" there would say nothing.
  const measured = device.rssi != null
    ? `${device.rssi} dBm`
    : device.linkQuality != null ? `LQI ${device.linkQuality}` : '';
  if (measured) {
    const metrics = document.createElement('small');
    metrics.className = 'topology-node-metrics';
    const seen = device.lastSeen ? relativeTime(new Date(device.lastSeen)) : '';
    metrics.textContent = seen ? `${measured} · ${seen}` : measured;
    if (linkIsStrained(device)) {
      metrics.classList.add('link-strained');
      metrics.title = `Retrying ${(device.frameErrorRate * 100).toFixed(0)}% of frames`;
    }
    node.append(metrics);
  }
  node.title = attachment ? `${attachment.source} parent link` : category === 'router' ? 'Thread router' : 'Parent unresolved';
  node.addEventListener('click', () => {
    state.topologySelectedId = state.topologySelectedId === device.id ? '' : device.id;
    renderTopology();
  });
  return node;
}

function topologyBadge(text, className) {
  const badge = document.createElement('em');
  badge.className = className;
  badge.textContent = text;
  return badge;
}

// renderTopologyInspector fills the floating device panel. The map used to give up a
// fixed column for this; showing it only while a device is selected keeps that width
// for the mesh itself.
function renderTopologyInspector(device, attachment = null) {
  const popover = document.getElementById('devicePopover');
  const body = document.getElementById('devicePopoverBody');
  body.replaceChildren();
  if (!device) {
    popover.classList.add('hidden');
    return;
  }
  popover.classList.remove('hidden');

  const heading = document.createElement('div');
  heading.className = 'topology-inspector-heading';
  const icon = document.createElement('span');
  icon.className = `device-symbol ${device.isBorderRouter ? 'border-router' : ''}`;
  icon.replaceChildren(makeIcon(device.isBorderRouter ? 'hexagon' : isTopologyRouter(device) ? 'router' : 'endDevice'));
  const identity = document.createElement('div');
  const name = document.createElement('h3');
  name.textContent = deviceLabel(device, device.id);
  const role = document.createElement('p');
  role.textContent = device.role || 'Unknown role';
  identity.append(name, role);
  heading.append(icon, identity);
  const ext = deviceExtendedAddress(device);
  if (ext) heading.append(renameButton(ext, device.customName, deviceLabel(device, device.id)));

  const list = document.createElement('dl');
  list.className = 'topology-inspector-list';
  list.append(
    topologyInspectorItem('RLOC16', device.rloc16),
    topologyInspectorItem('Router ID', device.routerId ?? routerIdFromRloc(device.rloc16)),
    topologyInspectorItem('Extended address', device.extendedAddress || (device.topologyDiagnostic ? null : device.id)),
    topologyInspectorItem('Parent', attachment?.device?.name || attachment?.device?.rloc16 || device.parent),
    topologyInspectorItem('Relationship', attachment?.source),
    topologyInspectorItem('OMR IPv6', device.omrIpv6Address || firstIPv6(device.ipv6Addresses)),
    topologyInspectorItem('Mesh-local', meshLocalAddress(device)),
    topologyInspectorItem('Link quality', device.rssi != null ? `${device.rssi} dBm` : device.linkQuality != null ? `LQI ${device.linkQuality}` : device.linkMargin != null ? `${device.linkMargin} dB margin` : null),
    topologyInspectorItem('Link margin', device.linkMargin == null ? null : `${device.linkMargin} dB`),
    topologyInspectorItem('Retries', linkHealth(device) || null, linkIsStrained(device) ? 'link-strained' : ''),
    topologyInspectorItem('Child timeout', device.timeout == null ? null : `${device.timeout}s`),
    topologyInspectorItem('Receiver', device.rxOnWhenIdle == null ? null : device.rxOnWhenIdle ? 'Always on' : 'Sleepy')
  );
  document.getElementById('devicePopoverTitle').textContent = deviceLabel(device, device.id);
  body.append(heading, list);
}

// rerender=false when the caller is about to redraw the map anyway.
function closeDevicePopover(rerender = true) {
  const had = state.topologySelectedId;
  state.topologySelectedId = '';
  document.getElementById('devicePopover').classList.add('hidden');
  if (rerender && had && state.deviceMode === 'map') renderTopology();
}

function topologyInspectorItem(label, value, valueClass = '') {
  const item = document.createElement('div');
  const term = document.createElement('dt');
  const description = document.createElement('dd');
  term.textContent = label;
  description.textContent = value === undefined || value === null || value === '' ? 'Unavailable' : String(value);
  description.title = description.textContent;
  if (valueClass) description.className = valueClass;
  item.append(term, description);
  return item;
}

function renderTopology() {
  const stage = document.getElementById('topologyStage');
  if (!stage) return;
  const devices = topologyDevices();
  const inventory = state.devices;
  const diagnosticTopology = Boolean(state.topology?.collectionSupported && (state.topology.nodes || []).length);
  // The local border router anchors the map: it is the one node the reader is
  // certain of, so it leads the first band rather than landing wherever its
  // router id happens to sort. The leader follows, then everything else in the
  // order the provider gave (sort is stable, so that order is preserved).
  const routerRank = device => (device.isBorderRouter ? 0 : device.role === 'leader' ? 1 : 2);
  const routers = devices.filter(isTopologyRouter)
    .sort((left, right) => routerRank(left) - routerRank(right));
  const children = devices.filter(device => !isTopologyRouter(device));
  const attachments = new Map();
  children.forEach(device => {
    const attachment = topologyParent(device, routers);
    if (attachment) attachments.set(device.id, attachment);
  });
  const attached = children.filter(device => attachments.has(device.id));
  const unresolved = children.filter(device => !attachments.has(device.id));

  document.getElementById('topologySource').textContent = diagnosticTopology
    ? `${state.topology.source} · ${state.topology.requestLatencyMs ?? '—'} ms`
    : inventory?.source ? `${inventory.source} · fallback relationships` : 'Reading device relationships…';
  const noteBox = document.getElementById('topologyNote');
  if (unresolved.length) {
    noteBox.querySelector('p').textContent = `${unresolved.length} device${unresolved.length === 1 ? '' : 's'} could not be attached to a known parent. Router-to-router radio adjacency is not exposed by this data source.`;
    noteBox.classList.remove('hidden');
  } else {
    noteBox.classList.add('hidden');
  }

  // Replacing the stage's children drops keyboard focus and any text selection in
  // the device panel, so only rebuild when something the drawing depends on changed.
  // The source label above is refreshed regardless, since latency moves every poll.
  // The layout depends on how much width is visible, so a resize must invalidate the
  // cache below or the wrapped rows would keep their old column count.
  const viewport = document.getElementById('topologyScroll');
  const measured = viewport ? viewport.clientWidth : 0;
  const available = measured > 320 ? measured : 1100;
  const columns = Math.max(1, Math.floor((available - 48) / 180));
  const signature = JSON.stringify([columns, devices, diagnosticTopology ? state.topology.links : null, [...attachments].map(([id, attachment]) => [id, attachment.device.id, attachment.source]), state.topologySelectedId, state.overview?.leaderRouterId ?? null]);
  if (signature === renderTopology.signature && stage.childElementCount) return;
  renderTopology.signature = signature;

  if (!devices.length) {
    const empty = document.createElement('div');
    empty.className = 'inventory-empty';
    const icon = document.createElement('span');
    icon.className = 'device-symbol';
    icon.replaceChildren(makeIcon('mesh'));
    const title = document.createElement('strong');
    title.textContent = inventory?.status === 'unavailable' ? 'Device map unavailable' : 'No device map data yet';
    const text = document.createElement('p');
    text.textContent = 'The map will appear when OTBR reports devices or the local border router becomes available.';
    empty.append(icon, title, text);
    stage.style.width = '100%';
    stage.style.height = '360px';
    stage.replaceChildren(empty);
    renderTopologyInspector(null);
    return;
  }

  if (state.topologySelectedId && !devices.some(device => device.id === state.topologySelectedId)) state.topologySelectedId = '';
  // One cluster per router: the router on top with its own children wrapped beneath
  // it. Grouping this way keeps every child link short and local — the previous
  // layered layout put all routers in one band and all children in another, so 36
  // children produced a dense fan of long crossing lines.
  const childrenByParent = new Map();
  attached.forEach(device => {
    const parentId = attachments.get(device.id)?.device?.id;
    if (!parentId) return;
    if (!childrenByParent.has(parentId)) childrenByParent.set(parentId, []);
    childrenByParent.get(parentId).push(device);
  });

  const width = available;
  const colPitch = 180;
  const rowPitch = 144;
  const nodeWidth = 154;
  const nodeHeight = TOPOLOGY_NODE_HEIGHT;
  const parentGap = 62; // router bottom to the first child row
  const clusterGapX = 40;
  const clusterGapY = 62;

  // Each cluster's natural width: a short row reads better than a square, and a
  // square better than a long line.
  const naturalCols = count => (count <= 4 ? Math.max(1, count) : Math.ceil(Math.sqrt(count)));
  const naturalWidth = routers.reduce((total, router) => {
    const kids = (childrenByParent.get(router.id) || []).length;
    return total + Math.max(1, naturalCols(kids)) * 180 + 40;
  }, 0);
  // Only squeeze when the natural layout genuinely does not fit. Capping
  // unconditionally wrapped three children onto two rows with the space to spare.
  const clustersPerBand = naturalWidth - 40 <= available - 48 ? 1 : 2;
  const maxClusterCols = clustersPerBand === 1
    ? columns
    : Math.max(1, Math.floor((columns - 1) / 2));
  const clusters = routers.map(router => {
    const kids = childrenByParent.get(router.id) || [];
    const cols = Math.min(maxClusterCols, naturalCols(kids.length));
    const rows = Math.ceil(kids.length / cols);
    return {
      router, kids, cols, rows,
      width: Math.max(colPitch, cols * colPitch),
      height: nodeHeight + (rows ? parentGap + (rows - 1) * rowPitch + nodeHeight : 0)
    };
  });

  // Pack clusters left to right, wrapping to a new band when the next will not fit.
  const positionById = new Map();
  let cursorX = 24;
  let bandTop = 78;
  let bandHeight = 0;
  clusters.forEach(cluster => {
    if (cursorX > 24 && cursorX + cluster.width > width - 24) {
      bandTop += bandHeight + clusterGapY;
      bandHeight = 0;
      cursorX = 24;
    }
    positionById.set(cluster.router.id, {
      x: Math.round(cursorX + (cluster.width - nodeWidth) / 2),
      y: bandTop
    });
    cluster.kids.forEach((kid, index) => {
      const row = Math.floor(index / cluster.cols);
      const col = index % cluster.cols;
      const inThisRow = Math.min(cluster.cols, cluster.kids.length - row * cluster.cols);
      const rowWidth = inThisRow * colPitch;
      const startX = cursorX + Math.round((cluster.width - rowWidth) / 2 + (colPitch - nodeWidth) / 2);
      positionById.set(kid.id, {
        x: startX + col * colPitch,
        y: bandTop + nodeHeight + parentGap + row * rowPitch
      });
    });
    bandHeight = Math.max(bandHeight, cluster.height);
    cursorX += cluster.width + clusterGapX;
  });
  let contentBottom = bandTop + bandHeight;

  const nodes = [];
  // Children whose parent could not be identified have no cluster to belong to.
  let unresolvedPositions = [];
  if (unresolved.length) {
    const unresolvedTop = contentBottom + 91;
    nodes.push(topologyLayerLabel('UNRESOLVED ATTACHMENTS', unresolvedTop - 51));
    unresolvedPositions = topologyGrid(unresolved.length, columns, width, unresolvedTop, rowPitch);
    contentBottom = unresolvedTop + (Math.ceil(unresolved.length / columns) - 1) * rowPitch + nodeHeight;
  }
  const routerPositions = routers.map(router => positionById.get(router.id));
  const childPositions = attached.map(device => positionById.get(device.id));
  const routerPositionById = positionById;

  if (diagnosticTopology) {
    (state.topology.links || []).filter(link => link.type === 'router').forEach(link => {
      const source = routerPositionById.get(link.source);
      const target = routerPositionById.get(link.target);
      if (!source || !target) return;
      const qualities = [link.linkQualityIn, link.linkQualityOut].filter(value => value != null).join('/');
      const details = [qualities ? `LQI ${qualities}` : '', link.linkMargin != null ? `${link.linkMargin} dB margin` : '', link.lastRssi != null ? `${link.lastRssi} dBm` : ''].filter(Boolean).join(' · ');
      nodes.push(topologyEdge(source, target, details ? `Router adjacency · ${details}` : 'Router adjacency', 'router'));
    });
  }
  attached.forEach((device, index) => {
    const attachment = attachments.get(device.id);
    const parentPosition = routerPositionById.get(attachment.device.id);
    if (parentPosition) {
      const quality = device.linkQuality == null ? '' : ` · LQI ${device.linkQuality}`;
      nodes.push(topologyEdge(parentPosition, childPositions[index], `${attachment.source}${quality}`, 'child'));
    }
  });
  routers.forEach((device, index) => nodes.push(topologyNode(device, routerPositions[index])));
  attached.forEach((device, index) => nodes.push(topologyNode(device, childPositions[index], attachments.get(device.id))));
  unresolved.forEach((device, index) => nodes.push(topologyNode(device, unresolvedPositions[index])));

  stage.style.width = `${width}px`;
  stage.style.height = `${contentBottom + 68}px`;
  stage.replaceChildren(...nodes);
  const selected = devices.find(device => device.id === state.topologySelectedId) || null;
  renderTopologyInspector(selected, selected ? attachments.get(selected.id) : null);
}

function renderDeviceRows() {
  const container = document.getElementById('deviceRows');
  const inventory = state.devices;
  if (!inventory) return;
  const query = state.deviceQuery.trim().toLowerCase();
  const filtered = (inventory.items || []).filter(device => {
    const category = roleCategory(device.role);
    const roleMatch = state.roleFilter === 'all' || category === state.roleFilter;
    const haystack = [device.customName, device.name, device.id, device.role, device.rloc16, device.extendedAddress, device.omrIpv6Address, ...(device.ipv6Addresses || [])].filter(Boolean).join(' ').toLowerCase();
    return roleMatch && (!query || haystack.includes(query));
  });
  if (!filtered.length) {
    const empty = document.createElement('div');
    empty.className = 'inventory-empty';
    const icon = document.createElement('span');
    icon.className = 'device-symbol';
    icon.replaceChildren(makeIcon('endDevice'));
    const title = document.createElement('strong');
    title.textContent = (inventory.items || []).length ? 'No devices match these filters' : 'No devices reported yet';
    const text = document.createElement('p');
    text.textContent = (inventory.items || []).length ? 'Try a different search term or role.' : inventory.collectionSupported ? 'OTBR returned an empty device collection. Discovery data may not have been collected yet.' : 'This OTBR version does not provide a device collection.';
    empty.append(icon, title, text);
    container.replaceChildren(empty);
    return;
  }
  const routers = (inventory.items || []).filter(isTopologyRouter);
  const nodes = [];
  filtered.forEach((device, index) => {
    const row = document.createElement('button');
    row.type = 'button';
    row.className = 'device-row';
    row.setAttribute('role', 'row');
    // The list is rebuilt on every poll because "last seen" moves each time, so
    // an open row would fall shut within five seconds unless the choice outlives
    // the element. It is keyed by device rather than position so it also
    // survives the device moving in the list.
    const expandKey = deviceExtendedAddress(device) || device.id || `#${index}`;
    const expanded = state.expandedDevices.has(expandKey);
    row.setAttribute('aria-expanded', String(expanded));
    row.setAttribute('aria-controls', `device-detail-${index}`);

    const primary = document.createElement('span');
    primary.className = 'device-cell device-primary';
    primary.setAttribute('role', 'cell');
    const symbol = document.createElement('span');
    symbol.className = `device-symbol ${device.isBorderRouter ? 'border-router' : ''}`;
    symbol.replaceChildren(makeIcon(device.isBorderRouter ? 'hexagon' : 'endDevice'));
    const identity = document.createElement('span');
    const name = document.createElement('b');
    name.textContent = deviceLabel(device, device.isBorderRouter ? 'Border Router' : `Thread device ${index + 1}`);
    const id = document.createElement('small');
    id.textContent = device.isBorderRouter ? `${device.id} · LOCAL OTBR` : device.id;
    identity.append(name, id);
    primary.append(symbol, identity);

    const category = roleCategory(device.role);
    const role = document.createElement('span');
    role.className = 'device-cell'; role.dataset.label = 'Role'; role.setAttribute('role', 'cell');
    const tag = document.createElement('span');
    tag.className = `device-tag ${device.role === 'leader' ? 'leader' : category}`;
    tag.textContent = device.role || 'Unknown';
    role.append(tag);

    const rloc = cell('RLOC16', device.rloc16 || '—', 'mono');
    // Who the device hangs off is the one relational fact in the row; the OMR
    // address it replaced is one click away in the expanded detail.
    let parentText = '—';
    if (!isTopologyRouter(device)) {
      const attachment = topologyParent(device, routers);
      parentText = attachment ? deviceLabel(attachment.device, parentFallback(attachment.device)) : device.parent || 'Unresolved';
    }
    const parent = cell('Parent', parentText);
    const linkText = device.rssi != null ? `${device.rssi} dBm` : device.linkQuality != null ? `LQI ${device.linkQuality}` : device.linkMargin != null ? `${device.linkMargin} dB` : 'Not reported';
    const link = cell('Link', linkText, linkText === 'Not reported' ? '' : 'link-value');
    // Retries ride under the signal, flagged the same way as on the map card, so a
    // link that looks strong by RSSI but is mostly retrying shows up without a click.
    if (device.frameErrorRate != null) {
      const retries = document.createElement('small');
      retries.className = `device-retries ${linkIsStrained(device) ? 'link-strained' : ''}`.trim();
      retries.textContent = `${(device.frameErrorRate * 100).toFixed(1)}% retried`;
      link.append(retries);
      link.title = `${linkText} · ${linkHealth(device)}`;
    }
    const observedText = device.lastSeen ? relativeTime(new Date(device.lastSeen)) : device.firstSeen ? `Discovered ${relativeTime(new Date(device.firstSeen))}` : 'Not reported';
    const observed = cell('Observed', observedText);
    row.append(primary, role, rloc, parent, link, observed);

    const detail = document.createElement('div');
    detail.className = `device-expanded ${expanded ? 'open' : ''}`.trim();
    detail.id = `device-detail-${index}`;
    detail.append(
      detailItem('MLEID IID', device.mlEidIid),
      detailItem('EUI-64', device.eui64),
      detailItem('Router ID', device.routerId),
      detailItem('OMR IPv6', device.omrIpv6Address || firstIPv6(device.ipv6Addresses)),
      detailItem('Thread version', device.threadVersion),
      detailItem('Link margin', device.linkMargin == null ? null : `${device.linkMargin} dB`),
      detailItem('Retries', linkHealth(device), linkIsStrained(device) ? 'link-strained' : ''),
      detailItem('Mesh-local', meshLocalAddress(device)),
      detailItem('IPv6 addresses', (device.ipv6Addresses || []).join('\n')),
      detailItem('First discovered', device.firstSeen ? new Date(device.firstSeen).toLocaleString() : null),
      detailItem('Last updated', device.lastSeen ? new Date(device.lastSeen).toLocaleString() : null),
      detailItem('Link quality / RSSI', [device.linkQuality == null ? null : `LQI ${device.linkQuality}`, device.rssi == null ? null : `${device.rssi} dBm`].filter(Boolean).join(' · '))
    );
    const listExt = deviceExtendedAddress(device);
    if (listExt) {
      const actions = document.createElement('div');
      actions.className = 'device-expanded-actions';
      actions.append(renameButton(listExt, device.customName, deviceLabel(device, device.id)));
      detail.append(actions);
    }
    row.addEventListener('click', () => {
      const open = row.getAttribute('aria-expanded') === 'true';
      row.setAttribute('aria-expanded', String(!open));
      detail.classList.toggle('open', !open);
      if (open) state.expandedDevices.delete(expandKey);
      else state.expandedDevices.add(expandKey);
    });
    nodes.push(row, detail);
  });
  container.replaceChildren(...nodes);
}

function cell(label, value, extraClass = '') {
  const node = document.createElement('span');
  node.className = `device-cell ${extraClass}`.trim();
  node.dataset.label = label;
  node.setAttribute('role', 'cell');
  node.textContent = value;
  node.title = value;
  return node;
}

function detailItem(label, value, valueClass = '') {
  const item = document.createElement('div');
  const heading = document.createElement('span');
  const content = document.createElement('strong');
  heading.textContent = label;
  content.textContent = value === undefined || value === null || value === '' ? 'Unavailable' : String(value);
  if (valueClass) content.className = valueClass;
  item.append(heading, content);
  return item;
}


// meshLocalAddress picks the address inside the mesh-local prefix. It is reachable
// only from within the Thread mesh — a browser on the LAN has no route to it — but
// unlike RLOC16 it survives roaming, so it is the stable handle for in-mesh
// diagnostics such as `ot-ctl ping` while RLOC16s churn.

// linkHealth renders the retry picture for a device's link to its parent. A high
// frame error rate alongside a strong RSSI points at interference or retries timing
// out against a sleep cycle, not at range — so the two are worth showing together.
function linkHealth(device) {
  const percent = value => `${(value * 100).toFixed(1)}%`;
  const parts = [];
  if (device.frameErrorRate != null) parts.push(`frames ${percent(device.frameErrorRate)}`);
  if (device.messageErrorRate != null) parts.push(`messages ${percent(device.messageErrorRate)}`);
  return parts.join(' · ');
}

// A link can look strong by RSSI while most frames need retrying; flag that rather
// than leaving the reader to spot it.
function linkIsStrained(device) {
  return device.frameErrorRate != null && device.frameErrorRate >= 0.25;
}

function meshLocalAddress(device) {
  const prefix = (state.overview?.meshLocalPrefix || '').split('/')[0].split('::')[0];
  const groups = prefix.toLowerCase().split(':').filter(Boolean);
  if (!groups.length) return '';
  const match = (device.ipv6Addresses || []).find(address => {
    const parts = address.toLowerCase().split(':');
    return groups.every((group, index) => parts[index] !== undefined && parseInt(parts[index], 16) === parseInt(group, 16));
  });
  if (match) return match;
  // The border router's own addresses come from the overview, not the mesh.
  return device.isBorderRouter ? state.overview?.meshLocalAddress || '' : '';
}

function firstIPv6(addresses = []) {
  return addresses.find(Boolean) || '';
}

// homeTitle names the page after the mesh it is showing; the network name only
// arrives with the first overview poll, so it falls back until then.
function homeTitle() {
  return state.overview?.networkName || 'Thread network';
}


// History is fetched on demand rather than polled: it is a diagnostic view, and the
// read costs a handful of socket round trips on the border router.
async function loadHistory() {
  const note = document.getElementById('historyNote');
  const neighbourRows = document.getElementById('neighborHistoryRows');
  try {
    const response = await fetch('/api/v1/history', { cache: 'no-store' });
    if (!response.ok) throw new Error(`History unavailable (${response.status})`);
    renderHistory((await response.json()).data);
  } catch (error) {
    note.classList.remove('hidden');
    note.querySelector('p').textContent = error.message;
    neighbourRows.replaceChildren(historyEmpty('Event history unavailable', 'The border router did not return its recorded events.'));
  }
}

function historyEmpty(title, text) {
  const empty = document.createElement('div');
  empty.className = 'inventory-empty';
  const heading = document.createElement('strong');
  heading.textContent = title;
  const body = document.createElement('p');
  body.textContent = text;
  empty.append(heading, body);
  return empty;
}

function historyRow(className, cells) {
  const row = document.createElement('div');
  row.className = className;
  row.setAttribute('role', 'row');
  cells.forEach(([text, extra]) => {
    const cell = document.createElement('span');
    cell.setAttribute('role', 'cell');
    if (extra) cell.className = extra;
    cell.textContent = text;
    row.append(cell);
  });
  return row;
}

function renderHistory(data) {
  const note = document.getElementById('historyNote');
  if (data.status !== 'available') {
    note.classList.remove('hidden');
    note.querySelector('p').textContent = data.error || 'Event history is unavailable.';
  } else {
    note.classList.add('hidden');
  }

  const neighbours = data.neighbors || [];
  const neighbourRows = document.getElementById('neighborHistoryRows');
  if (!neighbours.length) {
    neighbourRows.replaceChildren(historyEmpty('No recorded events', 'OpenThread keeps a fixed-size log; it is empty or was cleared by a restart.'));
  } else {
    neighbourRows.replaceChildren(...neighbours.map(entry => historyRow('history-row', [
      [`${relativeTime(new Date(entry.at))}`],
      [entry.customName || entry.extendedAddress || '—', 'mono'],
      // Added/Removed pairs seconds apart mean a device is flapping, not roaming.
      [`${entry.type || ''} ${entry.event || ''}`.trim()],
      [entry.rloc16 || '—', 'mono'],
      [entry.averageRssi == null ? '—' : `${entry.averageRssi} dBm`]
    ])));
  }

  const network = data.network || [];
  const networkRows = document.getElementById('networkHistoryRows');
  if (!network.length) {
    networkRows.replaceChildren(historyEmpty('No recorded changes', 'This border router has not changed role or partition within the retained log.'));
  } else {
    networkRows.replaceChildren(...network.map(entry => historyRow('history-row network', [
      [relativeTime(new Date(entry.at))],
      [entry.role || '—', 'capitalize'],
      [entry.rloc16 || '—', 'mono'],
      [entry.partitionId == null ? '—' : String(entry.partitionId), 'mono']
    ])));
  }
}

function syncNavigation() {
  closeTerm();
  closeDevicePopover(false);
  const requested = window.location.hash.slice(1);
  // #overview, #devices and #topology are the pre-merge hashes; they all land on home now.
  const active = ['network', 'help', 'manage', 'diagnostics'].includes(requested) ? requested : 'home';
  if (requested === 'topology') setDeviceMode('map');
  document.querySelectorAll('[data-nav]').forEach(link => {
    const selected = link.dataset.nav === active;
    link.classList.toggle('active', selected);
    if (selected) link.setAttribute('aria-current', 'page');
    else link.removeAttribute('aria-current');
  });
  document.querySelectorAll('[data-view]').forEach(view => {
    const selected = view.dataset.view === active;
    view.classList.toggle('hidden', !selected);
    view.setAttribute('aria-hidden', String(!selected));
  });
  const pageMeta = {
    home: [homeTitle(), 'THREAD MESH'],
    network: ['Nearby Networks', 'THREAD SCAN'],
    help: ['Help', 'THREAD GUIDE'],
    manage: ['Network Setup', 'MANAGE'],
    diagnostics: ['Diagnostics', 'EVENT HISTORY']
  };
  document.getElementById('pageTitle').textContent = pageMeta[active][0];
  // .hidden carries !important; the bare [hidden] attribute loses to .title-meta's display:flex.
  document.getElementById('titleMeta').classList.toggle('hidden', active !== 'home');
  document.getElementById('pageEyebrow').textContent = pageMeta[active][1];
  updateRefreshLabel();
  if (active === 'manage') loadNetworkConfig();
  if (active === 'diagnostics') loadHistory();
  if (active === 'home' && state.deviceMode === 'map') window.requestAnimationFrame(renderTopology);
  window.scrollTo({ top: 0, behavior: 'auto' });
}

function updateRefreshLabel() {
  const hash = window.location.hash;
  // Home leads with the mesh, so its freshness is the map's; the strip falls back to overview.
  const meshTimestamp = state.deviceMode === 'map'
    ? state.topology?.lastSuccessfulRefresh || state.devices?.lastSuccessfulRefresh
    : state.devices?.lastSuccessfulRefresh;
  const timestamp = hash === '#network'
    ? state.networkScan?.scannedAt
    : ['#help', '#manage', '#diagnostics'].includes(hash)
    ? state.overview?.lastSuccessfulRefresh
    : meshTimestamp || state.overview?.lastSuccessfulRefresh;
  const updated = document.querySelector('[data-updated]');
  updated.textContent = timestamp ? `Updated ${relativeTime(new Date(timestamp))}` : 'Waiting for first successful refresh';
}

function relativeTime(date) {
  const seconds = Math.max(0, Math.round((Date.now() - date.getTime()) / 1000));
  if (seconds < 5) return 'just now';
  if (seconds < 60) return `${seconds}s ago`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
  return `${Math.floor(seconds / 86400)}d ago`;
}

async function loadData(manual = false) {
  if (state.busy) return;
  state.busy = true;
  refreshDot.classList.add('spinning');
  try {
    const [overviewResponse, deviceResponse, topologyResponse] = await Promise.all([
      fetch('/api/v1/overview', { cache: 'no-store' }),
      fetch('/api/v1/devices', { cache: 'no-store' }),
      fetch('/api/v1/topology', { cache: 'no-store' })
    ]);
    if (!overviewResponse.ok || !deviceResponse.ok || !topologyResponse.ok) throw new Error('OTBR Insight API unavailable');
    const [overview, devices, topology] = await Promise.all([overviewResponse.json(), deviceResponse.json(), topologyResponse.json()]);
    renderOverview(overview.data);
    renderDevices(devices.data);
    renderTopologyData(topology.data);
    if (state.networkScan) renderNetworkScan(state.networkScan);
    if (manual) showToast('Network status refreshed');
  } catch (error) {
    const alert = document.getElementById('healthAlert');
    alert.classList.remove('hidden');
    document.getElementById('alertTitle').textContent = 'Dashboard connection interrupted';
    document.getElementById('alertText').textContent = 'OTBR Insight will retry automatically.';
  } finally {
    state.busy = false;
    refreshDot.classList.remove('spinning');
  }
}

// parentFallback names an unlabelled parent readably: the router id is the top six
// bits of the RLOC16, so "Router 2 · 0x0800" says the same thing as the bare hex
// while still carrying the exact identifier for cross-referencing ot-ctl output.
function parentFallback(router) {
  if (router.isBorderRouter) return 'Border Router';
  const routerId = router.routerId ?? routerIdFromRloc(router.rloc16);
  if (routerId == null) return router.rloc16 || router.id;
  return router.rloc16 ? `Router ${routerId} · ${router.rloc16}` : `Router ${routerId}`;
}

function deviceLabel(device, fallback) {
  return device.customName || device.name || fallback;
}

// deviceExtendedAddress returns the stable identifier a name can attach to, or
// null for diagnostic-only topology children whose id is synthetic.
function deviceExtendedAddress(device) {
  if (device.extendedAddress) return device.extendedAddress;
  if (device.topologyDiagnostic) return null;
  return device.id || null;
}

function renameButton(ext, currentName, displayName) {
  const button = document.createElement('button');
  button.type = 'button';
  button.className = 'rename-button';
  button.textContent = currentName ? 'Rename' : 'Add name';
  button.addEventListener('click', event => {
    event.stopPropagation();
    openRename(ext, currentName || '', displayName);
  });
  return button;
}

function openRename(ext, currentName, displayName) {
  const popover = document.getElementById('renamePopover');
  state.renamingExt = ext;
  document.getElementById('renameTarget').textContent = displayName || ext;
  const input = document.getElementById('renameInput');
  input.value = currentName;
  document.getElementById('renameClear').classList.toggle('hidden', !currentName);
  document.getElementById('renameError').classList.add('hidden');
  popover.classList.remove('hidden');
  input.focus();
  input.select();
}

function closeRename() {
  document.getElementById('renamePopover').classList.add('hidden');
  state.renamingExt = null;
}

async function renameDevice(ext, name) {
  const trimmed = name.trim();
  const response = await fetch(`/api/v1/devices/${encodeURIComponent(ext)}/name`, {
    method: trimmed ? 'PUT' : 'DELETE',
    headers: trimmed ? { 'Content-Type': 'application/json' } : undefined,
    body: trimmed ? JSON.stringify({ name: trimmed }) : undefined,
    cache: 'no-store'
  });
  if (!response.ok) {
    const payload = await response.json().catch(() => ({}));
    throw new Error(payload.error || 'The name could not be saved.');
  }
}

async function submitRename(name) {
  const ext = state.renamingExt;
  if (!ext) return;
  try {
    await renameDevice(ext, name);
    closeRename();
    showToast(name.trim() ? 'Device name saved' : 'Device name removed');
    await loadData();
  } catch (error) {
    const note = document.getElementById('renameError');
    note.textContent = error.message;
    note.classList.remove('hidden');
  }
}

async function loadNetworkConfig() {
  try {
    const response = await fetch('/api/v1/network', { cache: 'no-store' });
    const payload = await response.json();
    if (!response.ok) throw new Error(payload.error || 'Unavailable');
    renderNetworkConfig(payload.data);
  } catch (error) {
    document.getElementById('manageStateLabel').textContent = 'Network status unavailable';
  }
}

function renderNetworkConfig(config) {
  state.network = config;
  const dataset = config.dataset || {};
  const running = config.state && config.state !== 'disabled';
  document.getElementById('manageStateLabel').textContent = running
    ? `Online · ${config.state}`
    : dataset.present ? 'Configured · disabled' : 'No network configured';
  const set = (key, value) => { document.querySelector(`[data-net="${key}"]`).textContent = value; };
  set('networkName', dataset.networkName || '—');
  set('channel', dataset.channel ?? '—');
  set('panId', dataset.panId || '—');
  set('extPanId', dataset.extPanId || '—');
  hideCredentials();
  const credsCell = document.querySelector('[data-net="creds"]');
  credsCell.replaceChildren();
  if (dataset.hasNetworkKey) {
    const hidden = document.createElement('span');
    hidden.className = 'creds-hidden';
    hidden.textContent = 'Network key set · hidden';
    const reveal = document.createElement('button');
    reveal.type = 'button';
    reveal.className = 'rename-button creds-reveal-button';
    reveal.textContent = 'Reveal';
    reveal.addEventListener('click', () => {
      if (document.getElementById('credsPanel').classList.contains('hidden')) revealCredentials(reveal);
      else hideCredentials();
    });
    credsCell.append(hidden, reveal);
  } else {
    credsCell.textContent = 'Not set';
  }
  const toggle = document.getElementById('toggleStateButton');
  toggle.disabled = !dataset.present;
  toggle.textContent = running ? 'Disable' : 'Enable';
  toggle.classList.toggle('is-enable', !running);

  const banner = document.getElementById('restoreBanner');
  if (config.backup && config.backup.networkName) {
    const when = config.backup.savedAt ? relativeTime(new Date(config.backup.savedAt)) : '';
    document.getElementById('restoreInfo').textContent = `"${config.backup.networkName}" backed up${when ? ` · ${when}` : ''}. Restore it to bring those devices back.`;
    banner.classList.remove('hidden');
  } else {
    banner.classList.add('hidden');
  }
}

// revealCredentials fetches the unmasked dataset on demand and renders it with
// copy buttons. The values live only in the transient DOM, never in `state`.
async function revealCredentials(trigger) {
  const panel = document.getElementById('credsPanel');
  panel.replaceChildren();
  try {
    const response = await fetch('/api/v1/network/credentials', { cache: 'no-store' });
    const payload = await response.json();
    if (!response.ok) throw new Error(payload.error || 'Unavailable');
    const creds = payload.data || {};
    const rows = [
      ['Network key', creds.networkKey],
      ['PSKc', creds.pskc],
      ['Extended PAN ID', creds.extPanId],
      ['Dataset (TLV)', creds.tlv]
    ].filter(([, value]) => value);
    if (!rows.length) {
      panel.textContent = 'No credentials are set on this network.';
    } else {
      rows.forEach(([label, value]) => panel.append(credentialRow(label, value)));
    }
    panel.classList.remove('hidden');
    if (trigger) trigger.textContent = 'Hide';
  } catch (error) {
    panel.textContent = error.message;
    panel.classList.remove('hidden');
  }
}

function hideCredentials() {
  const panel = document.getElementById('credsPanel');
  panel.replaceChildren();
  panel.classList.add('hidden');
  const button = document.querySelector('.creds-reveal-button');
  if (button) button.textContent = 'Reveal';
}

function credentialRow(label, value) {
  const row = document.createElement('div');
  row.className = 'creds-row';
  const name = document.createElement('span');
  name.className = 'creds-label';
  name.textContent = label;
  const val = document.createElement('code');
  val.className = 'creds-value mono';
  val.textContent = value;
  const copy = document.createElement('button');
  copy.type = 'button';
  copy.className = 'copy-button';
  copy.textContent = 'Copy';
  copy.setAttribute('aria-label', `Copy ${label}`);
  copy.addEventListener('click', async () => {
    try {
      await navigator.clipboard.writeText(value);
      showToast(`${label} copied`);
    } catch (_) {
      showToast('Clipboard access unavailable');
    }
  });
  row.append(name, val, copy);
  return row;
}

// parsePanId reads a PAN ID as hex, with or without a 0x prefix — the notation
// OTBR and the rest of this app use — and returns a number or null for blank;
// NaN signals an invalid entry the caller should reject. Treating a digits-only
// entry as decimal would silently turn "1234" into 0x04d2.
function parsePanId(raw) {
  const value = String(raw || '').trim();
  if (!value) return null;
  const digits = value.replace(/^0x/i, '');
  return /^[0-9a-f]{1,4}$/i.test(digits) ? parseInt(digits, 16) : NaN;
}

async function controlRequest(method, path, body) {
  const response = await fetch(path, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined,
    cache: 'no-store'
  });
  if (!response.ok) {
    const payload = await response.json().catch(() => ({}));
    throw new Error(payload.error || 'The request failed.');
  }
}

function showFormError(key, message) {
  const note = document.querySelector(`[data-err="${key}"]`);
  if (!note) return;
  note.textContent = message;
  note.classList.toggle('hidden', !message);
}

// Appended to every warning about devices dropping off the mesh. Sleepy
// (battery) devices only re-attach on their own poll cycle or when woken.
const BATTERY_NOTE = ' Battery-powered devices may take several minutes to rejoin and might need a button press to wake.';

let confirmAction = null;

function openConfirm(title, text, confirmLabel, action) {
  confirmAction = action;
  document.getElementById('confirmTitle').textContent = title;
  document.getElementById('confirmText').textContent = text;
  document.getElementById('confirmGo').textContent = confirmLabel;
  document.getElementById('confirmError').classList.add('hidden');
  document.getElementById('confirmBackdrop').classList.remove('hidden');
  document.getElementById('confirmPopover').classList.remove('hidden');
  document.getElementById('confirmCancel').focus();
}

function closeConfirm() {
  confirmAction = null;
  document.getElementById('confirmBackdrop').classList.add('hidden');
  document.getElementById('confirmPopover').classList.add('hidden');
}

async function runConfirmAction() {
  if (!confirmAction) return;
  const go = document.getElementById('confirmGo');
  go.disabled = true;
  try {
    await confirmAction();
    closeConfirm();
    await loadNetworkConfig();
    await loadData();
  } catch (error) {
    const note = document.getElementById('confirmError');
    note.textContent = error.message;
    note.classList.remove('hidden');
  } finally {
    go.disabled = false;
  }
}

function showToast(message) {
  const toast = document.getElementById('toast');
  toast.textContent = message;
  toast.classList.add('show');
  window.clearTimeout(showToast.timeout);
  showToast.timeout = window.setTimeout(() => toast.classList.remove('show'), 1800);
}

document.getElementById('refreshButton').addEventListener('click', () => loadData(true));
document.getElementById('scanNetworksButton').addEventListener('click', scanNetworks);
document.getElementById('scanChannelsButton').addEventListener('click', scanChannels);
document.getElementById('refreshHistoryButton').addEventListener('click', loadHistory);
document.getElementById('termClose').addEventListener('click', () => closeTerm(true));
document.addEventListener('keydown', event => {
  if (event.key !== 'Escape') return;
  if (!document.getElementById('confirmPopover').classList.contains('hidden')) {
    closeConfirm();
    return;
  }
  if (!document.getElementById('renamePopover').classList.contains('hidden')) {
    closeRename();
    return;
  }
  if (!document.getElementById('devicePopover').classList.contains('hidden')) {
    closeDevicePopover();
    return;
  }
  closeTerm(true);
});
document.getElementById('renameClose').addEventListener('click', () => closeRename());
document.getElementById('renameForm').addEventListener('submit', event => {
  event.preventDefault();
  submitRename(document.getElementById('renameInput').value);
});
document.getElementById('renameClear').addEventListener('click', () => submitRename(''));

document.getElementById('confirmCancel').addEventListener('click', closeConfirm);
document.getElementById('confirmGo').addEventListener('click', runConfirmAction);
document.getElementById('confirmBackdrop').addEventListener('click', closeConfirm);

document.getElementById('toggleStateButton').addEventListener('click', () => {
  const running = state.network?.state && state.network.state !== 'disabled';
  const enabling = !running;
  openConfirm(
    enabling ? 'Enable Thread interface?' : 'Disable Thread interface?',
    enabling ? 'The border router will attach to its configured network.' : 'The border router will detach and attached devices will disconnect.' + BATTERY_NOTE,
    enabling ? 'Enable' : 'Disable',
    () => controlRequest('PUT', '/api/v1/network/state', { enabled: enabling })
  );
});

document.getElementById('formNetworkForm').addEventListener('submit', event => {
  event.preventDefault();
  showFormError('form', '');
  const form = event.target;
  const networkName = form.networkName.value.trim();
  if (!networkName) return showFormError('form', 'Network name is required.');
  const body = { networkName };
  if (form.channel.value) body.channel = Number(form.channel.value);
  if (form.panId.value.trim()) {
    const panId = parsePanId(form.panId.value);
    if (Number.isNaN(panId)) return showFormError('form', 'PAN ID must be 1–4 hex digits, e.g. 0xa1b2.');
    if (panId !== null) body.panId = panId;
  }
  openConfirm(
    'Form a new network?',
    `This replaces any current network on the border router with a brand-new "${networkName}". Attached devices will disconnect.${BATTERY_NOTE}`,
    'Form network',
    async () => { await controlRequest('POST', '/api/v1/network/form', body); form.reset(); showToast('Network formed'); }
  );
});

document.getElementById('joinNetworkForm').addEventListener('submit', event => {
  event.preventDefault();
  showFormError('join', '');
  const form = event.target;
  const networkName = form.networkName.value.trim();
  const networkKey = form.networkKey.value.trim();
  if (!networkName || !networkKey) return showFormError('join', 'Network name and key are required.');
  if (!/^(0x)?[0-9a-f]{32}$/i.test(networkKey)) return showFormError('join', 'Network key must be 32 hex characters.');
  const body = { networkName, networkKey };
  if (form.channel.value) body.channel = Number(form.channel.value);
  const panId = parsePanId(form.panId.value);
  if (Number.isNaN(panId)) return showFormError('join', 'PAN ID must be 1–4 hex digits, e.g. 0xa1b2.');
  if (panId !== null) body.panId = panId;
  if (form.extPanId.value.trim()) body.extPanId = form.extPanId.value.trim();
  const pskc = form.pskc.value.trim();
  if (pskc) {
    if (!/^(0x)?[0-9a-f]{32}$/i.test(pskc)) return showFormError('join', 'PSKc must be 32 hex characters.');
    body.pskc = pskc;
  }
  if (form.meshLocalPrefix.value.trim()) body.meshLocalPrefix = form.meshLocalPrefix.value.trim();
  openConfirm(
    'Join this network?',
    `This replaces any current network on the border router and attaches to "${networkName}". Attached devices will disconnect.${BATTERY_NOTE}`,
    'Join network',
    async () => { await controlRequest('POST', '/api/v1/network/join', body); form.reset(); showToast('Joining network'); }
  );
});

document.querySelectorAll('[data-join-mode]').forEach(button => button.addEventListener('click', () => {
  const mode = button.dataset.joinMode;
  document.querySelectorAll('[data-join-mode]').forEach(b => {
    const on = b === button;
    b.classList.toggle('active', on);
    b.setAttribute('aria-pressed', String(on));
  });
  document.getElementById('joinNetworkForm').classList.toggle('hidden', mode !== 'fields');
  document.getElementById('joinTlvForm').classList.toggle('hidden', mode !== 'tlv');
}));

document.getElementById('joinTlvForm').addEventListener('submit', event => {
  event.preventDefault();
  showFormError('tlv', '');
  const form = event.target;
  const tlv = form.tlv.value.replace(/\s+/g, '');
  if (!tlv) return showFormError('tlv', 'Paste an operational dataset TLV.');
  if (!/^(0x)?[0-9a-f]+$/i.test(tlv) || tlv.replace(/^0x/i, '').length % 2 !== 0) {
    return showFormError('tlv', 'Dataset must be an even-length hexadecimal string.');
  }
  openConfirm(
    'Join from dataset?',
    'This replaces any current network on the border router with the pasted dataset. Attached devices will disconnect.' + BATTERY_NOTE,
    'Join from dataset',
    async () => { await controlRequest('POST', '/api/v1/network/join/tlv', { tlv }); form.reset(); showToast('Joining network'); }
  );
});

document.getElementById('restoreButton').addEventListener('click', () => {
  const name = state.network?.backup?.networkName || 'the saved network';
  openConfirm(
    'Restore previous network?',
    `This puts the border router back on "${name}" and replaces the current network. Devices from that network will reconnect; devices on the current network will drop.${BATTERY_NOTE}`,
    'Restore network',
    async () => { await controlRequest('POST', '/api/v1/network/restore'); showToast('Restoring previous network'); }
  );
});

document.getElementById('leaveNetworkButton').addEventListener('click', () => {
  openConfirm(
    'Leave the current network?',
    'Thread will be disabled and the active dataset cleared. All attached devices will disconnect. This cannot be undone from here.' + BATTERY_NOTE,
    'Leave network',
    async () => { await controlRequest('DELETE', '/api/v1/network'); showToast('Left network'); }
  );
});
document.addEventListener('click', event => {
  const popover = document.getElementById('termPopover');
  if (popover.classList.contains('hidden') || popover.contains(event.target) || event.target.closest('.term-help-button')) return;
  closeTerm();
});
document.getElementById('devicePopoverClose').addEventListener('click', () => closeDevicePopover());
document.addEventListener('click', event => {
  const popover = document.getElementById('devicePopover');
  if (popover.classList.contains('hidden') || popover.contains(event.target) || event.target.closest('.topology-node')) return;
  if (event.target.closest('.rename-popover')) return;
  closeDevicePopover();
});
document.getElementById('deviceSearch').addEventListener('input', event => {
  state.deviceQuery = event.target.value;
  renderDeviceRows();
});
document.getElementById('roleFilter').addEventListener('change', event => {
  state.roleFilter = event.target.value;
  renderDeviceRows();
});
document.querySelectorAll('[data-device-mode]').forEach(button => button.addEventListener('click', () => {
  setDeviceMode(button.dataset.deviceMode);
}));
function applyTheme(theme) {
  document.documentElement.dataset.theme = theme;
  const meta = document.querySelector('meta[name="theme-color"]');
  if (meta) meta.content = theme === 'light' ? '#f2f5f7' : '#0b1016';
}

document.getElementById('themeButton').addEventListener('click', () => {
  const next = document.documentElement.dataset.theme === 'light' ? 'dark' : 'light';
  applyTheme(next);
  localStorage.setItem('otbr-insight-theme', next);
});

document.querySelectorAll('[data-copy]').forEach(button => button.addEventListener('click', async () => {
  const value = state.overview?.[button.dataset.copy];
  if (!value) return showToast('No value available');
  try {
    await navigator.clipboard.writeText(String(value));
    showToast('Copied to clipboard');
  } catch (_) {
    showToast('Clipboard access unavailable');
  }
}));

const savedTheme = localStorage.getItem('otbr-insight-theme');
const prefersLight = window.matchMedia('(prefers-color-scheme: light)');
if (savedTheme === 'light' || savedTheme === 'dark') {
  applyTheme(savedTheme);
} else if (prefersLight.matches) {
  applyTheme('light');
}
prefersLight.addEventListener('change', event => {
  if (!localStorage.getItem('otbr-insight-theme')) applyTheme(event.matches ? 'light' : 'dark');
});
decorateTerms();
window.addEventListener('hashchange', syncNavigation);
window.addEventListener('resize', () => {
  window.clearTimeout(renderTopology.resizeTimeout);
  renderTopology.resizeTimeout = window.setTimeout(renderTopology, 120);
});
syncNavigation();
loadData();
window.setInterval(() => loadData(), 5000);
window.setInterval(updateRefreshLabel, 1000);
