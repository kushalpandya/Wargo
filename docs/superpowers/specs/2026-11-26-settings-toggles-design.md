# Settings header switches and visual consistency

## Intent

Settings cards that turn a feature on or off do it with a switch in the
card header, the way the Agents (MCP) card does. Off collapses the card to
its header and keeps every saved value, so switching back on restores the
feature as it was. The owner also wants the settings pages to look like
one system.

## Decisions (owner approved)

- **Shared pattern.** A header switch on a card; off shows the header only;
  nothing saved is lost. One shared implementation in the card header.
- **General > Updates.** "Update checks" becomes "Updates". Its header
  switch turns automatic checks on or off. One "Check frequency" row covers
  OS packages, Homebrew and container images (choices 6h, 12h, daily,
  3 days, weekly). Server: the three sources share one stored frequency
  plus a separate on/off value, so off never loses the frequency. A saved
  frequency of 0 ("Off") migrates to switched off with daily remembered;
  container checks join the shared frequency. "Check now" on the Updates
  page still works while switched off.
- **General > Agents.** The agent list uses the Passkeys list look: one
  bordered box with dividers. The solid red revoke icon button moves to the
  far right, after the access dropdown.
- **Appearance > Weather.** The header switch replaces "Show the weather on
  Home"; Units and Location show only while on.
- **Integrations.** Prowlarr, Transmission and Plex each get a header
  switch; the Disconnect buttons go. Off means Wargo acts as if that
  integration were not set up (pages and MCP tools hide, Transmission
  polling and auto-remove pause, search and queue stop) while its saved
  address and secret stay stored. Never set up: the switch is on and the
  card shows its setup form. There is no longer a button that wipes saved
  credentials; saving new ones replaces them.
- **Security.** Two-factor gets a header switch: on starts the existing
  setup dialog, off asks for the password as today; Recovery codes show
  only while on. The passkey delete button becomes solid red (danger), like
  the agents' revoke button. Card icons get tints: Two-factor mint,
  Passkeys iris, Active sessions sky.
- **Consistency pass.** Every settings card header has a tinted icon chip
  (integration logos stay). Same row type sizes and spacing, switches in
  headers rather than rows, the bordered list style for lists, same small
  button size inside cards, no dashes used as punctuation in copy.

## Approaches considered

- Header switch as a UI-only convention (each card wires its own switch):
  rejected, it drifts; one CardHeader prop keeps them identical.
- Updates "off" as frequency 0 (today's model): rejected, it loses the
  frequency. A separate on/off value is needed on the server.
- Integration "off" as Disconnect: rejected by the owner, it deletes the
  saved credentials.

## Left to the builder

Exact copy, the name of the server's on/off fields, and migration details,
within the decisions above.
