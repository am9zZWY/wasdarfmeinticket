<script>
  // Shows the outcome of a check: the answer for all tickets together, the
  // connection leg by leg, and the reasons per ticket.
  let { view, markdown } = $props();

  let copied = $state(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(markdown);
      copied = true;
      setTimeout(() => (copied = false), 2000);
    } catch {
      copied = false;
    }
  }
</script>

{#snippet icon(verdict)}
  {#if verdict === 'valid'}
    <path d="M3 8.5l3.2 3L13 4.5" />
  {:else if verdict === 'invalid'}
    <path d="M4 4l8 8M12 4l-8 8" />
  {:else if verdict === 'partial'}
    <path d="M3 8h10" />
  {:else}
    <path d="M6 6a2 2 0 1 1 2.6 1.9c-.4.2-.6.5-.6.9V10M8 12.5v.5" />
  {/if}
{/snippet}

<section aria-live="polite" class="fields">
  <header>
    <h1>{view.title}</h1>
    {#if view.when}
      <p>{view.when}</p>
    {/if}
  </header>

  <div class="verdict {view.verdict}">
    <svg width="24" height="24" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">{@render icon(view.verdict)}</svg>
    <div>
      <strong>{view.summary}</strong>
      {#if view.note}
        <p>{view.note}</p>
      {/if}
    </div>
  </div>

  <ol class="legs" aria-label="Abschnitte">
    {#each view.legs as leg}
      <li class="card leg" class:walk={leg.walk}>
        {#if leg.verdict}
          <span class="status {leg.verdict}">
            <svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">{@render icon(leg.verdict)}</svg>
          </span>
        {/if}
        <div class="leg-body">
          <span class="line">{leg.name}</span>
          <dl class="times">
            <dt>{leg.departure}</dt>
            <dd>{leg.from}</dd>
            <dt>{leg.arrival}</dt>
            <dd>{leg.to}</dd>
          </dl>
          {#if leg.localLine}
            <p class="hint">fährt auch als {leg.localLine} (Nahverkehr)</p>
          {/if}
          {#if leg.stretches}
            <ul class="stretches">
              {#each leg.stretches as stretch}
                <li class={stretch.tickets.length > 0 ? 'valid' : 'invalid'}>
                  <span>{stretch.name}</span>
                  <span>{stretch.tickets.length > 0 ? stretch.tickets.join('; ') : 'kein Ticket'}</span>
                </li>
              {/each}
            </ul>
          {/if}
          {#if leg.unknown?.length}
            <p class="hint">nicht prüfbar: {leg.unknown.join('; ')}</p>
          {/if}
          {#if leg.zones}
            <p class="hint">Zonen: {leg.zones}</p>
          {/if}
        </div>
      </li>
    {/each}
  </ol>

  {#if view.missing?.length}
    <h3>Noch ohne Ticket</h3>
    <ul class="missing">
      {#each view.missing as stretch}
        <li>{stretch}</li>
      {/each}
    </ul>
  {/if}

  <h3>Tickets</h3>
  {#each view.tickets as ticket}
    <details class="card">
      <summary>
        <span>{ticket.label}</span>
        <span class="badge {ticket.verdict}">{ticket.verdictText}</span>
      </summary>
      <ul class="reasons">
        {#each ticket.legs as leg}
          <li>
            <strong>{leg.name}</strong>
            <span class="badge {leg.verdict}">{leg.verdictText}</span>
            <p>{leg.reason}</p>
          </li>
        {/each}
      </ul>
    </details>
  {/each}

  <p class="hint">Ohne Gewähr. Verbindlich sind die Tarifbestimmungen der Verkehrsunternehmen.</p>
  <button type="button" class="quiet" onclick={copy}>{copied ? 'Kopiert' : 'Ergebnis als Text kopieren'}</button>
</section>
