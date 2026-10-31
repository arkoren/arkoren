package rediscache

// decrementMinimumScript handles subtraction of MinInt64 without negating it in
// Go or Lua. Native DECRBY rejects that argument even for representable results.
// Negative counters can instead add MaxInt64, then one, atomically; neither step
// can overflow. Native INCRBY validates the stored integer and retains its TTL.
// Missing/nonnegative counters use native DECRBY's nonmutating overflow error.
// GET returns an exact decimal string rather than a rounded Lua numeric reply.
const decrementMinimumScript = `
local value = redis.call('GET', KEYS[1])
if value and string.sub(value, 1, 1) == '-' then
  redis.call('INCRBY', KEYS[1], '9223372036854775807')
  redis.call('INCRBY', KEYS[1], '1')
  return redis.call('GET', KEYS[1])
end
return redis.call('DECRBY', KEYS[1], ARGV[1])
`
