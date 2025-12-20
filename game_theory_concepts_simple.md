# Game Theory Concepts: Simple Guide

## Nash Equilibrium

This is when everyone has found their best move and nobody wants to change. It is like when cars at a four-way stop all figure out who should go first, and once they settle into a pattern, nobody tries to cut in front because that would just cause problems. In your microservices, Nash equilibrium is when each service has chosen its strategy and no service can improve by changing what it does.

**Example:** Three gateways routing to backends. If each gateway sends thirty percent of traffic to Backend A, forty percent to Backend B, and thirty percent to Backend C, and no gateway can get better performance by changing its mix, that is Nash equilibrium.

## No-Regret Learning

This means learning to make better choices by tracking which decisions you wish you had made differently. Every time you make a choice, you look back and think whether other options would have been better. Over time, you favor the options you keep wishing you had picked. The math guarantees that eventually you will perform almost as well as if you had known the best choice from the start.

**Example:** Your load balancer sends a request to Backend A and it takes two hundred milliseconds. You check and see Backend B would have taken fifty milliseconds. You regret not using B. Next time, you are more likely to try B because of that regret.

## Correlated Equilibrium

This is when a central coordinator sends suggestions to everyone, and everyone benefits from following the suggestions. It is like a traffic light telling different cars when to go. The light coordinates them so they do not crash. Each driver follows the light because they know others are following their complementary signals, making it safe to obey.

**Example:** Your service mesh tells Gateway One to prefer Backend A, Gateway Two to prefer Backend B, and Gateway Three to prefer Backend C. Each gateway follows the suggestion because it knows this spreads the load evenly, which benefits everyone.

## The Folk Theorem

This explains why cooperation can last over time even though cheating would be profitable right now. When you interact repeatedly, the threat of future punishment makes current cooperation worthwhile. If you cheat once, others will punish you forever after, making the long-term cost greater than the short-term gain. This only works if you care enough about the future.

**Example:** Two services agree to respect each other's rate limits. If Service A violates the agreement and floods Service B, then Service B will permanently throttle all requests from Service A. Knowing this punishment is coming, Service A cooperates even though breaking the rules would help in the moment.

## Regret Matching

This is the specific algorithm for learning from regret. You keep a score for each option showing how much you regret not using it. When making your next choice, you pick options randomly but give higher probability to options with higher regret scores. This simple rule has mathematical guarantees that it will eventually find good strategies.

**Example:** After one hundred requests, your regret scores are Backend A equals fifty, Backend B equals negative twenty, Backend C equals ten. You only consider A and C since B has negative regret. You pick A with probability fifty divided by sixty equals eighty-three percent and C with probability ten divided by sixty equals seventeen percent.

## Punishment Level

This is the worst outcome other players can force on you if they all team up against you. It sets a floor on what you should accept in any cooperation agreement. If someone asks you to accept less than your punishment level, you should refuse because you can guarantee yourself better by acting alone and defending optimally even when everyone opposes you.

**Example:** If all other services try to overload your backend, you can still guarantee five hundred requests per second by implementing rate limiting. So your punishment level is five hundred requests per second. Any cooperation deal that gives you less than this is a bad deal.

## Feasible Payoffs

These are outcomes that are actually possible to achieve if everyone coordinates perfectly. Think of it as the set of all results you could get if everyone worked together optimally. Some outcomes are impossible no matter what anyone does, so they are not feasible. Game theory focuses on feasible outcomes because there is no point discussing impossible scenarios.

**Example:** Your system has three backends with total capacity of one thousand requests per second. Feasible payoffs include any distribution where total load does not exceed one thousand. A scenario requiring fifteen hundred requests per second is not feasible.

## Individually Rational

This means each player gets at least their punishment level. A deal is individually rational if everyone involved gets more than they could guarantee themselves by going it alone. If a deal is not individually rational for someone, they will refuse to participate because they can do better by themselves.

**Example:** A cooperation scheme asks Service A to handle six hundred requests per second, but Service A's punishment level is seven hundred. This is not individually rational for Service A, so they will not cooperate. They are better off breaking the deal and defending themselves.

## Subgame Perfect Equilibrium

This is a stronger version of Nash equilibrium that requires strategies to remain optimal even after someone makes a mistake or deviates from the plan. It eliminates threats that are not credible because carrying them out would hurt the person making the threat. This ensures that all threatened punishments are actually believable.

**Example:** Service A threatens to shut down completely if Service B ever sends too much traffic. But shutting down would hurt Service A more than just dealing with the traffic. So this threat is not credible, and Service B will not believe it. Subgame perfection requires only credible threats like temporary throttling that actually makes sense to enforce.

## Mixed Strategy

This means randomizing your choices instead of always doing the same thing. Randomization makes you unpredictable, which prevents others from exploiting patterns in your behavior. This is especially important in adversarial situations where someone might be trying to game your system. If you are predictable, you can be exploited.

**Example:** Your rate limiter does not always check requests at exactly one thousand per second. Sometimes it checks at nine hundred, sometimes at eleven hundred, chosen randomly. This prevents attackers from learning your exact threshold and staying just under it.

## Bayesian Game

This is when players have private information that others do not know. Each player knows their own situation but only has beliefs about others' situations. You make decisions based on your private knowledge and your best guesses about what others know. As you observe behavior, you update your beliefs about what others might know.

**Example:** Backend A knows it is running in degraded mode after a partial failure, but gateways do not know this. Gateways only see that A is responding slowly. They update their belief that A might be unhealthy based on the slow responses, and gradually send less traffic to A.

## Zero-Sum Game

This is when one player's gain is exactly another player's loss. If I win, you lose by the same amount. These games model pure conflict with no room for cooperation. The optimal strategy is often to randomize to avoid being predictable. Zero-sum games are useful for modeling security scenarios where you face an attacker.

**Example:** You are defending against a denial-of-service attack. Resources you spend blocking attacks are resources the attacker wastes. If you block ninety percent of attack traffic, the attacker loses ninety percent effectiveness. Your gain is their loss.

## Potential Game

This is a special type of game where there is a function that captures everyone's incentives simultaneously. When any player improves their situation, this function increases. The nice property is that such games always reach equilibrium because the function cannot increase forever. This gives you guaranteed stability.

**Example:** A routing game where each gateway wants low latency. You create a potential function that sums everyone's latency plus congestion costs. When any gateway finds a better route, the potential increases. Eventually no improvements are possible and you have reached equilibrium.

## Repeated Game

This is when the same interaction happens over and over between the same players. Repetition changes everything because current actions affect future interactions. Players can build reputations, establish trust, punish bad behavior, and reward cooperation. Many one-shot problems that seem unsolvable become manageable with repetition.

**Example:** Two services interact thousands of times per day for months. If one service cheats, the other can punish it for weeks, making cheating unprofitable. Because they interact repeatedly, both services cooperate even though defecting would help in any single interaction.

## Convergence

This means the system eventually settles down to a stable state instead of bouncing around forever. When your services are learning and adapting, you want their strategies to converge to an equilibrium rather than oscillating endlessly. Different learning algorithms have different convergence properties, and some games converge faster than others.

**Example:** Your gateways start with random routing decisions, then learn from latency observations. At first, routing patterns change a lot. After a few hours, patterns stabilize with each gateway sending roughly the same mix to each backend. The system has converged to equilibrium.

## Lyapunov Function

This is a mathematical tool for proving convergence. It is a function that always decreases over time until reaching equilibrium, like water flowing downhill until it reaches the lowest point. If you can find such a function for your system, you have proven it will eventually stabilize. It provides a way to measure progress toward equilibrium.

**Example:** In a potential game, the potential function itself is a Lyapunov function. As services adjust their strategies, the potential increases until reaching a maximum at equilibrium. Monitoring this function tells you whether the system is converging or stuck.

---

**The Big Picture:** These concepts work together to help you design systems where independent services naturally cooperate and create stable, efficient outcomes. You use Nash equilibrium to understand where systems settle, no-regret learning to adapt over time, correlated equilibrium to coordinate through signals, and repeated games to sustain cooperation. The math guarantees these approaches work.
