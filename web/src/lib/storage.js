// The user's tickets are kept on the device, in the browser's local storage.
const ticketsKey = 'wasdarfmeinticket.tickets';

export function loadTickets() {
  try {
    const tickets = JSON.parse(localStorage.getItem(ticketsKey));
    return Array.isArray(tickets) ? tickets : [];
  } catch {
    return [];
  }
}

export function saveTickets(tickets) {
  try {
    localStorage.setItem(ticketsKey, JSON.stringify(tickets));
  } catch {
    // Private mode or full storage: the tickets last until the page closes.
  }
}
