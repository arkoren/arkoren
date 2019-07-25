import { User } from '../../user.ts'
import { Request, Response } from '../../../../framework/http.ts'

export namespace MainController {

    /**
     * Index page for the controller.
     *
     * @export
     * @param {Request} req
     * @returns {Response}
     */
    export function index(req: Request): Response {
        return { name: 'Erik', age: 22 }
    }

    /**
     * Another page for the controller.
     *
     * @export
     * @param {Request} req
     * @returns {Response}
     */
    export function another(req: Request): Response {
        const name = (new User).getTable()
        return `<h1>User model table: ${name}</h1>`
    }

    /**
     * A page with two parameters.
     *
     * @export
     * @param {Request} req
     * @param {string} name
     * @param {string} page
     * @returns {Response}
     */
    export function book(req: Request, name: string, page: string): Response {
        return `<h1>Book: ${name}, page ${page}</h1>`
    }

}
