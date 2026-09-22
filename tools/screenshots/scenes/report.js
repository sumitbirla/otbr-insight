// Scene: the Device report view, populated from the sanitised export fixture.
//
// The file goes through the real POST /api/v1/fabrics/identify, so the image
// shows what the decoder actually produces rather than a hand-built mock.
(async () => {
  location.hash = '#report';
  const body = __FIXTURE__;
  const response = await fetch('/api/v1/fabrics/identify', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body
  });
  const payload = await response.json();
  if (!response.ok) throw new Error(payload.error);
  state.deviceReport = payload.data;
  renderDeviceReport(state.deviceReport);
  syncNavigation();
  // Open a couple of sections so the shot shows what a section holds, not only
  // the collapsed index.
  const wanted = ['Health and uptime'];
  for (const card of document.querySelectorAll('#reportBody .report-card')) {
    if (wanted.includes(card.querySelector('.disclosure-title strong').textContent)) card.open = true;
  }
  syncReportToolbar();
  return 'ok';
})()
