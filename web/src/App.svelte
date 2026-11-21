<script>
  import { onMount } from 'svelte';
  import { loadEngine } from './lib/engine.js';
  import Result from './lib/Result.svelte';
  import { loadTickets, saveTickets } from './lib/storage.js';
  import Tickets from './lib/Tickets.svelte';

  let engine = $state(null);
  let engineError = $state('');

  let tickets = $state(loadTickets());
  let mode = $state('link'); // how the connection is given: link or stops
  let link = $state(sharedText());
  let pastedAnswer = $state('');
  let from = $state('');
  let to = $state('');
  let departure = $state(nextFiveMinutes());

  let running = $state(false);
  let progress = $state([]);
  let outcome = $state(null);
  let failure = $state('');

  onMount(async () => {
    try {
      engine = await loadEngine();
    } catch (error) {
      engineError = String(error?.message ?? error);
    }
  });

  $effect(() => saveTickets(tickets));

  // What the pasted text is, as soon as the engine can tell.
  let linkInfo = $derived(engine && link.trim() ? engine.describeLink(link) : { kind: 'empty' });

  // The request for the engine, or the reason why it cannot be sent yet.
  let plan = $derived.by(() => {
    if (tickets.length === 0) {
      return { problem: 'Füge mindestens ein Ticket hinzu.' };
    }
    if (mode === 'stops') {
      if (!from.trim() || !to.trim() || !departure) {
        return { problem: 'Gib Start, Ziel und Abfahrtszeit an.' };
      }
      return { request: { from: from.trim(), to: to.trim(), departure, tickets } };
    }
    if (linkInfo.kind === 'empty') {
      return { problem: 'Füge einen Link von bahn.de ein.' };
    }
    if (linkInfo.kind === 'none') {
      return { problem: 'Darin ist kein Link von bahn.de zu erkennen.' };
    }
    if (linkInfo.kind === 'share') {
      if (!pastedAnswer.trim()) {
        return { problem: 'Füge die Antwort von bahn.de ein (siehe oben).' };
      }
      return { request: { recon: pastedAnswer, tickets } };
    }
    return { request: { link, tickets } };
  });

  async function check(event) {
    event.preventDefault();
    if (!engine || !plan.request || running) {
      return;
    }
    running = true;
    progress = [];
    outcome = null;
    failure = '';
    try {
      outcome = await engine.check($state.snapshot(plan.request), (message) => (progress = [...progress, message]));
    } catch (error) {
      failure = String(error?.message ?? error);
    } finally {
      running = false;
    }
  }

  // When the app is opened through the system's share menu, the shared
  // link arrives in the page address.
  function sharedText() {
    const shared = new URLSearchParams(location.search);
    const text = ['url', 'text', 'title'].map((name) => shared.get(name)).filter(Boolean).join(' ');
    if (text) {
      history.replaceState(null, '', location.pathname);
    }
    return text;
  }

  // The current time, rounded up to five minutes, as the input field wants it.
  function nextFiveMinutes() {
    const fiveMinutes = 5 * 60 * 1000;
    const time = new Date(Math.ceil(Date.now() / fiveMinutes) * fiveMinutes);
    const pad = (number) => String(number).padStart(2, '0');
    return `${time.getFullYear()}-${pad(time.getMonth() + 1)}-${pad(time.getDate())}T${pad(time.getHours())}:${pad(time.getMinutes())}`;
  }
</script>

<main>
  <header>
    <h1>Was darf mein Ticket?</h1>
    <p>Prüft, auf welchen Abschnitten einer Verbindung deine Tickets gelten.</p>
  </header>

  {#if engineError}
    <p class="verdict invalid" role="alert">Die App konnte nicht starten: {engineError}</p>
  {/if}

  <form onsubmit={check}>
    <section>
      <h2>Verbindung</h2>
      <div class="modes" role="group" aria-label="Verbindung angeben über">
        <button type="button" class:active={mode === 'link'} aria-pressed={mode === 'link'} onclick={() => (mode = 'link')}>Link</button>
        <button type="button" class:active={mode === 'stops'} aria-pressed={mode === 'stops'} onclick={() => (mode = 'stops')}>Start und Ziel</button>
      </div>

      {#if mode === 'link'}
        <label>
          Link von bahn.de oder aus dem DB Navigator
          <textarea bind:value={link} rows="3" placeholder="https://www.bahn.de/buchung/…"></textarea>
        </label>
        {#if linkInfo.kind === 'share'}
          <div class="card">
            <p>
              Das ist ein geteilter Link. Die Bahn lässt Webseiten ihn nicht nachschlagen, dein Browser darf es aber:
            </p>
            <ol>
              <li><a href={linkInfo.lookupURL} target="_blank" rel="noopener">Antwort von bahn.de öffnen</a></li>
              <li>Dort den ganzen Text kopieren.</li>
              <li>Hier einfügen:</li>
            </ol>
            <label>
              Antwort von bahn.de
              <textarea bind:value={pastedAnswer} rows="3" placeholder={'{"hinfahrtRecon": …'}></textarea>
            </label>
            <p class="hint">
              Ohne diesen Umweg geht es mit der Adresse, die bahn.de bei geöffneter Verbindung in der Adresszeile zeigt.
            </p>
          </div>
        {:else if linkInfo.kind === 'search'}
          <p class="hint">Suchlink: Es wird die erste Verbindung ab der gesuchten Zeit genommen.</p>
        {/if}
      {:else}
        <label>
          Von
          <input bind:value={from} placeholder="Haltestelle" />
        </label>
        <label>
          Nach
          <input bind:value={to} placeholder="Haltestelle" />
        </label>
        <label>
          Abfahrt
          <input type="datetime-local" bind:value={departure} />
        </label>
      {/if}
    </section>

    <Tickets bind:tickets laendertickets={engine?.laendertickets ?? []} labelOf={(ticket) => engine?.ticketLabel(ticket) || ticket.type} />

    <button type="submit" class="primary" disabled={!engine || running || Boolean(plan.problem)}>
      {#if !engine && !engineError}
        Wird geladen …
      {:else if running}
        Prüfe …
      {:else}
        Prüfen
      {/if}
    </button>
    {#if engine && plan.problem}
      <p class="hint">{plan.problem}</p>
    {/if}
  </form>

  {#if running || (failure && progress.length > 0)}
    <ul class="progress" aria-live="polite">
      {#each progress as message}
        <li>{message}</li>
      {/each}
    </ul>
  {/if}

  {#if failure}
    <p class="verdict invalid" role="alert">{failure}</p>
  {/if}

  {#if outcome}
    <Result view={outcome.view} markdown={outcome.markdown} />
  {/if}

  <footer>
    <p>
      Fahrplandaten: <a href="https://transitous.org" target="_blank" rel="noopener">Transitous</a> (DELFI, CC-BY-4.0). Zonen und Tarife: Auskünfte
      der Verkehrsverbünde. Alle Angaben ohne Gewähr.
    </p>
    <p>Deine Tickets bleiben auf diesem Gerät. Anfragen gehen direkt von hier an die Auskünfte.</p>
  </footer>
</main>
