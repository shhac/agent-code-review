// Latest request wins: for panels that re-ask the daemon whenever an input
// changes. Answers can land out of order, and an answer to an earlier question
// shown beside the current inputs is the one thing such a panel must never do.
//
// The token is taken the moment ask is CALLED, not when the request goes out.
// Taking it at send time left the whole debounce window in which an in-flight
// answer to the previous question still counted as current, so it landed
// under the new inputs. Two panels each carried that fix separately.

// latestWins returns an ask function. Each call supersedes every earlier one
// at once, then runs work after delayMs (immediately when 0, so a caller
// without a debounce still starts its request synchronously). Only the newest
// call's outcome reaches onValue or onError; a superseded one is dropped,
// failures included. work runs when the delay elapses, so it reads the inputs
// as they are then.
export function latestWins(delayMs = 0) {
  let asked = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;

  return function ask<T>(work: () => Promise<T>, onValue: (value: T) => void, onError: (error: unknown) => void) {
    const mine = ++asked;
    clearTimeout(timer);
    const run = async () => {
      try {
        const value = await work();
        if (mine === asked) onValue(value);
      } catch (error) {
        if (mine === asked) onError(error);
      }
    };
    if (delayMs > 0) timer = setTimeout(run, delayMs);
    else void run();
  };
}
