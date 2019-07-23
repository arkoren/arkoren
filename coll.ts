import { collect } from './arkoren/collections/collections.ts'

class Person {
    age: number
    constructor(age: number) {
        this.age = age
    }
}

const collection = collect([1, 2, 3, 4, 5, 6, 7])
const collection2 = collect([ { value: 10 }, { value: 20 }, { value: 30 } ])
const collection3 = collect([ new Person(22), new Person(18) ])
const collection4 = collect([1, null, false, true, 20])

const res = collection
    .map(el => el * 2)
    .chunk(4)
    .collapse<number>()
    .map(item => item)
    .filter(value => value > 5)
    .first(val => val == 10)
const res2 = collection2
    .filter(value => value.value > 15)
    .sum('value')
const res3 = collection3
    .sum('age')
const res4 = collection4
    .filter()
    .toArray()

console.log(res)
console.log(res2)
console.log(res3)
console.log(res4)
