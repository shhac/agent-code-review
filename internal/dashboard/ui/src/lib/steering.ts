// One copy of the steering message limit on the client.
//
// It mirrors store.SteeringMaxLen. The server is authoritative and rejects an
// over-long message with a 400, so this only decides when the textarea stops
// accepting keystrokes and what the remaining-characters counter says.
export const MAX_STEERING = 2000;
