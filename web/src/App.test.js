import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import App from './App.svelte';
import { loadEngine } from './lib/engine.js';

vi.mock('./lib/engine.js', () => ({ loadEngine: vi.fn() }));

const view = {
  title: 'Rottenburg → Horb',
  when: 'Mo 23.11.2026, 10:45–11:10',
  verdict: 'partial',
  summary: 'Deine Tickets decken die Verbindung nur teilweise ab.',
  legs: [
    {
      name: 'RB74',
      walk: false,
      from: 'Rottenburg',
      departure: '10:45',
      to: 'Horb',
      arrival: '11:10',
      verdict: 'partial',
      stretches: [
        { name: 'Rottenburg → Eyach', tickets: ['Verbundticket naldo, Zonen 12'] },
        { name: 'Eyach → Horb', tickets: [] },
      ],
      zones: 'naldo 12, 13 (Tarifauskunft EFA-BW)',
    },
  ],
  missing: ['RB74: Eyach → Horb'],
  tickets: [
    {
      label: 'Verbundticket naldo, Zonen 12',
      verdict: 'partial',
      verdictText: 'teilweise gültig',
      legs: [{ name: 'RB74 Rottenburg → Horb', verdict: 'partial', verdictText: 'teilweise gültig', reason: 'gültig von Rottenburg bis Eyach' }],
    },
  ],
};

let engine;

beforeEach(() => {
  localStorage.clear();
  history.replaceState(null, '', '/');
  engine = {
    check: vi.fn(async (request, onProgress) => {
      onProgress('Tickets prüfen (1)');
      return { view, markdown: '# Rottenburg → Horb' };
    }),
    describeLink: vi.fn((text) => {
      if (text.includes('vbid=')) {
        return { kind: 'share', lookupURL: 'https://www.bahn.de/web/api/angebote/verbindung/abc' };
      }
      return text.includes('bahn.de') ? { kind: 'search' } : { kind: 'none' };
    }),
    ticketLabel: (ticket) => (ticket.type === 'deutschlandticket' ? 'Deutschlandticket' : `Verbundticket ${ticket.net}`),
    laendertickets: [{ id: 'bayern-ticket', label: 'Bayern-Ticket' }],
  };
  loadEngine.mockResolvedValue(engine);
});

afterEach(cleanup);

const checkButton = () => screen.findByRole('button', { name: 'Prüfen' });

test('checks a connection given by start and destination', async () => {
  render(App);
  await fireEvent.click(await screen.findByRole('button', { name: 'Ticket hinzufügen' }));
  expect(await screen.findByText('Deutschlandticket', { selector: 'li span' })).toBeTruthy();

  await fireEvent.click(screen.getByRole('button', { name: 'Start und Ziel' }));
  await fireEvent.input(screen.getByLabelText('Von'), { target: { value: 'Rottenburg' } });
  await fireEvent.input(screen.getByLabelText('Nach'), { target: { value: 'Horb' } });
  await fireEvent.input(screen.getByLabelText('Abfahrt'), { target: { value: '2026-11-23T10:45' } });
  await fireEvent.click(await checkButton());

  await waitFor(() => expect(engine.check).toHaveBeenCalledTimes(1));
  expect(engine.check.mock.calls[0][0]).toEqual({
    from: 'Rottenburg',
    to: 'Horb',
    departure: '2026-11-23T10:45',
    tickets: [{ type: 'deutschlandticket' }],
  });
  expect(await screen.findByText('Deine Tickets decken die Verbindung nur teilweise ab.')).toBeTruthy();
  expect(screen.getByText('kein Ticket')).toBeTruthy();
  expect(screen.getByText('RB74: Eyach → Horb')).toBeTruthy();
  expect(screen.getByText('gültig von Rottenburg bis Eyach')).toBeTruthy();
});

test('keeps the tickets on the device', async () => {
  const first = render(App);
  await fireEvent.click(await screen.findByRole('button', { name: 'Ticket hinzufügen' }));
  await waitFor(() => expect(JSON.parse(localStorage.getItem('wasdarfmeinticket.tickets'))).toEqual([{ type: 'deutschlandticket' }]));
  first.unmount();

  render(App);
  expect(await screen.findByText('Deutschlandticket', { selector: 'li span' })).toBeTruthy();
  await fireEvent.click(screen.getByRole('button', { name: 'Deutschlandticket entfernen' }));
  await waitFor(() => expect(JSON.parse(localStorage.getItem('wasdarfmeinticket.tickets'))).toEqual([]));
});

test('takes a link from the share menu and asks for the answer of bahn.de for a share link', async () => {
  localStorage.setItem('wasdarfmeinticket.tickets', JSON.stringify([{ type: 'deutschlandticket' }]));
  history.replaceState(null, '', '/?text=' + encodeURIComponent('Meine Reise https://www.bahn.de/buchung/start?vbid=abc'));
  render(App);

  const lookup = await screen.findByRole('link', { name: 'Antwort von bahn.de öffnen' });
  expect(lookup.getAttribute('href')).toBe('https://www.bahn.de/web/api/angebote/verbindung/abc');
  expect(location.search).toBe('');
  expect((await checkButton()).disabled).toBe(true);

  await fireEvent.input(screen.getByLabelText('Antwort von bahn.de'), { target: { value: '{"hinfahrtRecon":"x"}' } });
  await fireEvent.click(await checkButton());
  await waitFor(() => expect(engine.check).toHaveBeenCalledTimes(1));
  expect(engine.check.mock.calls[0][0]).toEqual({ recon: '{"hinfahrtRecon":"x"}', tickets: [{ type: 'deutschlandticket' }] });
});

test('sends other links as they are and shows a failure', async () => {
  localStorage.setItem('wasdarfmeinticket.tickets', JSON.stringify([{ type: 'deutschlandticket' }]));
  engine.check.mockRejectedValueOnce(new Error('keine Verbindung gefunden'));
  render(App);

  const link = 'https://www.bahn.de/buchung/fahrplan/suche#so=A&zo=B';
  await fireEvent.input(await screen.findByLabelText('Link von bahn.de oder aus dem DB Navigator'), { target: { value: link } });
  await fireEvent.click(await checkButton());

  expect((await screen.findByRole('alert')).textContent).toContain('keine Verbindung gefunden');
  expect(engine.check.mock.calls[0][0]).toEqual({ link, tickets: [{ type: 'deutschlandticket' }] });
});

test('describes a Verbund ticket by zones', async () => {
  render(App);
  await fireEvent.change(await screen.findByLabelText('Art'), { target: { value: 'verbund' } });
  const add = screen.getByRole('button', { name: 'Ticket hinzufügen' });
  expect(add.disabled).toBe(true);

  await fireEvent.input(screen.getByLabelText('Verbund'), { target: { value: 'naldo' } });
  await fireEvent.change(screen.getByLabelText('Gilt für'), { target: { value: 'zones' } });
  await fireEvent.input(screen.getByLabelText('Zonen'), { target: { value: '11, 220' } });
  await fireEvent.click(add);

  await waitFor(() =>
    expect(JSON.parse(localStorage.getItem('wasdarfmeinticket.tickets'))).toEqual([{ type: 'verbund', net: 'naldo', zones: ['11', '220'] }]),
  );
});
