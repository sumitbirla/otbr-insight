// Scene: the Channel noise view.
//
// The measurement is canned rather than scanned. A real sweep would publish this
// network's RF environment and its channel, and an energy scan takes the border
// router off channel for ten seconds — neither belongs in a screenshot build.
(() => {
  location.hash = '#channels';
  syncNavigation();
  // Canned measurement: a plausible 2.4 GHz picture with Wi-Fi 1/6/11 humps,
  // so the published image shows the view rather than this network's airwaves.
  // Channels are graded against the quietest in the same scan (<=3 dB quiet,
  // >10 dB busy), so these are spread to show all three verdicts: Wi-Fi 1/6/11
  // humps busy, their shoulders moderate, the gaps quiet. Channel 25 carries a
  // Channel 15 carries a loud peak from its own network's traffic while
  // sitting at the floor typically, which is why the current channel is
  // graded on its median rather than its peak.
  const levels = {
    11: [-86, -93], 12: [-72, -88], 13: [-68, -86], 14: [-84, -92],
    15: [-62, -94], 16: [-83, -91], 17: [-70, -87], 18: [-66, -85],
    19: [-85, -92], 20: [-91, -95], 21: [-80, -90], 22: [-71, -87],
    23: [-74, -88], 24: [-86, -93], 25: [-86, -93], 26: [-92, -96]
  };
  renderChannelScan({
    status: 'available',
    currentChannel: 15,
    sweeps: 3,
    source: 'OpenThread daemon socket',
    scannedAt: new Date().toISOString(),
    durationMs: 10400,
    channels: Object.entries(levels).map(([channel, [maxRssi, typicalRssi]]) =>
      ({ channel: Number(channel), maxRssi, typicalRssi }))
  });
  return 'ok';
})()
