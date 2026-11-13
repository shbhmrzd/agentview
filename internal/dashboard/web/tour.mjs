export const TOUR_KEY = 'agentview-tour-v1';

export function tourSeen(storage) {
  try { return storage?.getItem(TOUR_KEY) === 'seen'; } catch { return false; }
}
export function rememberTour(storage) {
  try { storage?.setItem(TOUR_KEY, 'seen'); } catch { /* The tour still works without browser storage. */ }
}

const steps = [
  {target: '.list-toolbar', title: 'Find a conversation with new activity', text: 'Choose “New activity (2 min)” to find conversations where a new message, tool call or turn event was observed. Send a message in Claude or Codex to make your conversation appear here. A green label is evidence of recent updates, not proof that a process is still running.'},
  {target: '#session-list', title: 'Choose the conversation', text: 'Each row groups a main conversation and its children. Search by request, project, or any agent ID. Activity from a child updates its whole conversation. Choose “All sources” under List view to inspect individual files. “Recent file activity” means the source changed recently; “No recent updates” means no new conversation event was observed in the last two minutes. Neither label means the agent has stopped.'},
  {target: '#agents-tab', title: 'Understand the agent family', text: 'Conversations with multiple sources open Agent tree, showing recorded parent → child branches, the first request and latest reply for each agent. Expand a card for excerpts and provenance, or choose Open trace for the full conversation. Missing links remain explicit; a latest reply may still be work in progress.'},
  {target: '.detail-tabs', title: 'Follow new activity', text: 'History shows an earlier snapshot. Live activity follows new messages and tool events without refreshing the page. Once you choose Live activity, switching between agents keeps you in that view.'},
  {target: '#trace-filters', title: 'See the request. Inspect the result.', text: 'In your trace, choose Tools and open a Tool call or Tool result. Here is the same interaction with sample data — both cards are expanded so you can see exactly what is inside.'},
  {target: '#metrics-tab', title: 'Inspect usage and outcomes', text: 'Metrics shows available token usage, model details, tool calls, flagged errors and timing. Missing data stays unavailable. Use “Take a tour” any time you want this walkthrough again.'},
];

// This walkthrough only highlights navigation. It never changes the selected
// session, switches providers, sends agent messages, or creates source events.
// The payload example is static markup inside the dialog, separate from data.
export function createTour({document, storage, getContext = () => ({})}) {
  const get = id => document.getElementById(id);
  const dialog = get('tour-dialog');
  let index = -1, considered = false, highlighted = null, previousFocus = null;
  const scrollPositions = new Map();

  function clearHighlight() { highlighted?.classList.remove('tour-step-focus'); highlighted = null; }
  function highlight() {
    clearHighlight();
    if (index < 0) return;
    let target = document.querySelector(steps[index].target);
    if (!target?.getClientRects().length) target = document.querySelector('.detail-panel');
    if (!target) return;
    highlighted = target; target.classList.add('tour-step-focus');
    for (let node = target.parentElement; node; node = node.parentElement) {
      if (!scrollPositions.has(node)) scrollPositions.set(node, [node.scrollLeft, node.scrollTop]);
    }
    // On narrow screens the card sits below the highlighted control. Align
    // the control at the top so it cannot remain hidden behind that card.
    target.scrollIntoView({block: document.documentElement.clientWidth <= 740 ? 'start' : 'nearest', inline: 'nearest', behavior: 'instant'});
  }
  function render() {
    const welcome = index < 0, demo = index === 4, context = getContext();
    dialog.classList.toggle('tour-running', !welcome);
    dialog.classList.toggle('tour-demo-step', demo);
    get('tour-meter').hidden = welcome;
    get('tour-meter').value = index + 1;
    get('tour-progress').textContent = welcome ? 'WELCOME TO AGENTVIEW' : `STEP ${index + 1} OF ${steps.length}`;
    get('tour-title').textContent = welcome ? 'Find your agents. Follow their work.' : steps[index].title;
    get('tour-description').textContent = welcome ? 'Take a quick tour of conversations, agent relationships, live updates, and tool payloads. You can skip it and reopen it whenever you need.' : steps[index].text;
    get('tour-workflow').hidden = !welcome;
    get('tour-payload-demo').hidden = !demo;
    if (demo) { get('demo-request').open = true; get('demo-result').open = true; }
    get('tour-back').hidden = index <= 0;
    get('tour-next').textContent = welcome ? 'Start tour' : index === steps.length - 1 ? 'Finish' : 'Next';
    let note = '';
    if (demo) note = context.includeContent ? 'For a spawn call, the result may contain only a child ID. Open that child to read its answer. Coordination shows requests; Tools includes the results too.' : 'This example is always available. To inspect your own payloads, start AgentView with --include-content.';
    else if (!context.sessions && index >= 1) note = 'No sessions are visible yet. These controls become available when a transcript is discovered; you can still finish the tour.';
    get('tour-context').textContent = note; get('tour-context').hidden = !note;
    if (demo) clearHighlight(); else highlight();
    dialog.scrollTop = 0;
  }
  function open(start = -1) {
    if (dialog.open) return;
    previousFocus = document.activeElement; considered = true; index = start;
    dialog.showModal(); render(); get('tour-next').focus();
  }
  function finish() {
    rememberTour(storage); considered = true;
    clearHighlight(); dialog.close();
    for (const [node, [left, top]] of scrollPositions) { node.scrollLeft = left; node.scrollTop = top; }
    scrollPositions.clear();
    if (previousFocus?.isConnected && previousFocus !== document.body) previousFocus.focus({preventScroll:true});
    else get('take-tour').focus({preventScroll:true});
  }
  function next() {
    rememberTour(storage);
    if (index === steps.length - 1) { finish(); return; }
    index++; render(); get('tour-next').focus();
  }
  get('take-tour').addEventListener('click', () => open());
  get('payload-guide').addEventListener('click', () => open(4));
  get('tour-payload-shortcut').addEventListener('click', () => { rememberTour(storage); index = 4; render(); get('tour-next').focus(); });
  get('tour-close').addEventListener('click', finish);
  get('tour-skip').addEventListener('click', finish);
  get('tour-next').addEventListener('click', next);
  get('tour-back').addEventListener('click', () => { if (index > 0) { index--; render(); } });
  dialog.addEventListener('cancel', event => { event.preventDefault(); finish(); });
  return {
    maybeStart() {
      if (considered || document.querySelector('dialog[open]')) return;
      considered = true;
      if (!tourSeen(storage)) open();
    },
  };
}
