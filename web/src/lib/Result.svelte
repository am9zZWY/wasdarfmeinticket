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

<section aria-live="polite">
  <h2>{view.title}</h2>
  {#if view.when}
    <p class="when">{view.when}</p>
  {/if}

  <p class="verdict {view.verdict}">{view.summary}</p>
  {#if view.note}
    <p class="hint">{view.note}</p>
  {/if}

  <h3>Verbindung</h3>
  <ol class="legs">
    {#each view.legs as leg}
      <li class="card">
        <p class="leg">
          <strong>{leg.name}</strong>
          <span>{leg.from} {leg.departure} → {leg.to} {leg.arrival}</span>
        </p>
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
      </li>
    {/each}
  </ol>

  {#if view.missing?.length}
    <h3>Noch ohne Ticket</h3>
    <ul>
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

  <button type="button" class="quiet" onclick={copy}>{copied ? 'Kopiert' : 'Ergebnis als Text kopieren'}</button>
</section>
